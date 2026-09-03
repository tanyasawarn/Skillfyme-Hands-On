import { Inject, Injectable, Logger } from '@nestjs/common';
import { Kysely } from 'kysely';
import { KYSELY } from '../../db/database.module';
import type { ActivityMode, Database } from '../../db/schema';
import { EventStoreRepository } from '../event-store/event-store.repository';
import { appendTypedEvent } from '../event-store/attempt-event-type';
import { LlmGatewayService } from '../llm-gateway/llm-gateway.service';
import { EnvStateSummaryService } from './env-state-summary.service';
import {
  DisclosureCeiling,
  resolveDisclosure,
  type DisclosureResolution,
} from './disclosure-policy';
import { checkOutput } from './output-guardrail';
import { classifyIntent, type Intent } from './intent-classifier';
import { activePrompt } from './prompt-registry';

/**
 * PLAN.md G4 / doc §7.2 -- the Mentor Service. Layered pipeline
 * (doc §7.2 diagram):
 *
 *   1. POLICY RESOLUTION  -> persona + disclosure_ceiling (G5)
 *   2. INTENT CLASSIFY     -> concept_q | error_help | just_tell_me |
 *                            off_topic | injection
 *   3. CONTEXT ASSEMBLY    -> activity summary + env-state summary (G2) +
 *                            mastery + conversation history.
 *                            SOLUTION IS NOT AVAILABLE (G3 -- this
 *                            service cannot import SolutionStore).
 *   4. LLM GATEWAY CALL    -> routed/cached/budgeted (G1)
 *   5. OUTPUT GUARDRAIL    -> disclosure check + command/solution leak
 *                            detector (G5). Over-ceiling content is
 *                            redacted or the reply is regenerated as a
 *                            question.
 *   6. ACCOUNTING          -> AI_MESSAGE event with prompt version,
 *                            tokens, cost, policy decision, guardrail
 *                            verdict; HINT_REQUESTED-style integrity
 *                            signal on an injection attempt.
 *
 * The mentor has NO write access to the environment (doc §7.7): it only
 * reads (EnvStateSummaryService) and replies. On any hard failure it
 * degrades to "ask a question / suggest the authored hint" -- never
 * blocks the attempt.
 */
@Injectable()
export class MentorService {
  private readonly logger = new Logger(MentorService.name);

  constructor(
    @Inject(KYSELY) private readonly db: Kysely<Database>,
    private readonly events: EventStoreRepository,
    private readonly gateway: LlmGatewayService,
    private readonly envState: EnvStateSummaryService,
  ) {}

  async reply(input: MentorReplyInput): Promise<MentorReply> {
    const attempt = await this.db
      .selectFrom('attempt.attempt as a')
      .innerJoin(
        'content.activity_version as av',
        'av.id',
        'a.activity_version_id',
      )
      .select([
        'a.id as attempt_id',
        'a.user_id',
        'a.tenant_id',
        'a.mode',
        'av.spec_jsonb',
      ])
      .where('a.id', '=', input.attemptId)
      .executeTakeFirst();

    if (!attempt) {
      return this.degrade('attempt not found', 'concept_q');
    }
    const mode = attempt.mode as ActivityMode;
    const spec = (attempt.spec_jsonb ?? {}) as {
      objectives?: string[];
      tasks?: Array<{ key: string; title?: string; instructions_md?: string }>;
      ai_mentor?: { disclosure_ceiling?: number; concept_notes?: string };
      faults?: Array<{ canonical_diagnostic_path?: string }>;
    };

    // 1. POLICY RESOLUTION
    const meanMastery = await this.meanMasteryForAttempt(input.attemptId);
    const disclosure = resolveDisclosure({
      mode,
      meanMastery,
      timeStuckMinutes: input.timeStuckMinutes ?? 0,
      hintLevelReached: input.hintLevelReached,
      activityOverride:
        spec.ai_mentor?.disclosure_ceiling != null
          ? spec.ai_mentor.disclosure_ceiling
          : undefined,
    });

    // 2. INTENT CLASSIFY
    const intent = classifyIntent(input.message);

    if (intent === 'injection') {
      await this.recordIntegritySignal(input.attemptId, input.message);
      return {
        text:
          "That looks like an attempt to change how I work, so I won't follow it. " +
          "Let's stick to the task — what have you tried so far?",
        intent,
        disclosure,
        guardrailViolations: [],
        degraded: false,
        promptVersion: activePrompt().id,
        costUsd: 0,
      };
    }
    if (intent === 'off_topic') {
      return {
        text: "That's outside this activity. If it's about the concept you're working on, ask me that directly.",
        intent,
        disclosure,
        guardrailViolations: [],
        degraded: false,
        promptVersion: activePrompt().id,
        costUsd: 0,
      };
    }

    // 3. CONTEXT ASSEMBLY -- no solution path exists here (G3)
    const env = await this.envState.summarize(input.attemptId, {
      recentCommands: 30,
    });
    const sensitiveResourceNames = this.sensitiveNamesFrom(env);

    const canonicalPath =
      disclosure.mayUseCanonicalPath &&
      spec.faults?.[0]?.canonical_diagnostic_path
        ? spec.faults[0].canonical_diagnostic_path
        : undefined;

    const contextBlock = JSON.stringify({
      objectives: spec.objectives ?? [],
      tasks: (spec.tasks ?? []).map((t) => ({
        key: t.key,
        title: t.title,
        instructions: t.instructions_md,
      })),
      concept_notes: spec.ai_mentor?.concept_notes ?? null,
      env_state: env,
      learner_mean_mastery: meanMastery,
      canonical_diagnostic_path: canonicalPath ?? null,
    });

    const system = activePrompt()
      .systemPrompt.replace('{{persona}}', disclosure.persona)
      .replace('{{disclosure_ceiling}}', String(disclosure.ceiling))
      .replace('{{activity_spec_summary}}', contextBlock)
      .replace('{{concept_notes}}', spec.ai_mentor?.concept_notes ?? '')
      .replace('{{env_state_summary}}', JSON.stringify(env))
      .replace('{{learner_mastery}}', String(meanMastery))
      .replace('{{conversation_history}}', input.history ?? '');

    const user = `<untrusted-learner-content>${input.message}</untrusted-learner-content>`;

    // 4. LLM GATEWAY CALL
    const res = await this.gateway.call({
      taskClass: intent === 'error_help' ? 'mentor_reply' : 'mentor_reply',
      promptVersion: activePrompt().id,
      system,
      user,
      attemptId: attempt.attempt_id,
      userId: attempt.user_id,
      tenantId: attempt.tenant_id,
    });

    if (res.degraded) {
      return this.degrade(`gateway ${res.degraded}`, intent, disclosure);
    }

    // 5. OUTPUT GUARDRAIL
    const guard = checkOutput({
      text: res.text,
      ceiling: disclosure.ceiling,
      maxCodeLines: disclosure.maxCodeLines,
      sensitiveResourceNames,
    });

    const finalText = guard.allowed
      ? guard.redacted
      : 'I can point you in the right direction but not hand over the fix. What does the failing check tell you about where to look?';

    // 6. ACCOUNTING
    await appendTypedEvent(this.events, {
      attemptId: input.attemptId,
      actor: 'AI',
      type: 'AI_MESSAGE',
      payload: {
        role: 'assistant',
        tokens: res.inputTokens + res.outputTokens,
        policy_decision: {
          persona: disclosure.persona,
          disclosure_ceiling: disclosure.ceiling,
          intent,
        },
        guardrail_verdict: {
          allowed: guard.allowed,
          violations: guard.violations,
        },
        prompt_version: res.promptVersion,
        cost_usd: res.costUsd,
        cache_hit: res.cacheHit,
      },
    });

    return {
      text: finalText,
      intent,
      disclosure,
      guardrailViolations: guard.violations,
      degraded: false,
      promptVersion: res.promptVersion,
      costUsd: res.costUsd,
    };
  }

  private degrade(
    reason: string,
    intent: Intent,
    disclosure?: DisclosureResolution,
  ): MentorReply {
    this.logger.warn(`mentor degraded: ${reason}`);
    return {
      text: "I can't reach my full context right now. Try the next authored hint on this task, or tell me exactly what you've observed and I'll help you reason about it.",
      intent,
      disclosure: disclosure ?? {
        persona: 'PATIENT_TUTOR',
        ceiling: DisclosureCeiling.ConceptOnly,
        mayUseCanonicalPath: false,
        maxCodeLines: 0,
      },
      guardrailViolations: [],
      degraded: true,
      promptVersion: activePrompt().id,
      costUsd: 0,
    };
  }

  private async meanMasteryForAttempt(attemptId: string): Promise<number> {
    const rows = await this.db
      .selectFrom('attempt.attempt as a')
      .innerJoin(
        'content.activity_skill as ask',
        'ask.activity_version_id',
        'a.activity_version_id',
      )
      .leftJoin('skill.skill_mastery as sm', (j) =>
        j
          .onRef('sm.skill_id', '=', 'ask.skill_id')
          .onRef('sm.user_id', '=', 'a.user_id'),
      )
      .select(['sm.p_mastery'])
      .where('a.id', '=', attemptId)
      .execute();
    if (rows.length === 0) return 0.5;
    const vals = rows.map((r) =>
      r.p_mastery == null ? 0.3 : Number(r.p_mastery),
    );
    return vals.reduce((s, v) => s + v, 0) / vals.length;
  }

  private sensitiveNamesFrom(env: {
    recentCommands: Array<{ cmd: string }>;
    resourceSummary: { changedFiles: string[] };
  }): string[] {
    const names = new Set<string>();
    // resource-name tokens seen in commands ("kubectl describe pod checkout-xyz")
    for (const c of env.recentCommands) {
      const m = c.cmd.match(
        /\b(?:deploy(?:ment)?|pod|svc|service|statefulset|daemonset|configmap|secret)\/?\s+([a-z0-9][a-z0-9.-]{2,})/gi,
      );
      if (m)
        for (const tok of m) names.add(tok.split(/\s+|\//).pop() as string);
    }
    return Array.from(names).filter((n) => n && n.length >= 3);
  }

  private async recordIntegritySignal(attemptId: string, message: string) {
    await appendTypedEvent(this.events, {
      attemptId,
      actor: 'SYSTEM',
      type: 'AI_MESSAGE',
      payload: {
        role: 'system',
        policy_decision: { intent: 'injection', action: 'refused' },
        integrity_signal: 'prompt_injection_attempt',
        excerpt: message.slice(0, 200),
      },
    });
    this.logger.warn(
      `prompt-injection attempt on attempt ${attemptId}: ${message.slice(0, 120)}`,
    );
  }
}

export interface MentorReplyInput {
  attemptId: string;
  message: string;
  /** minutes since last progress on the current task (drives disclosure). */
  timeStuckMinutes?: number;
  /** deepest hint level revealed on the current task (GUIDED_LAB). */
  hintLevelReached?: number;
  /** prior turns, pre-rendered. */
  history?: string;
}

export interface MentorReply {
  text: string;
  intent: Intent;
  disclosure: DisclosureResolution;
  guardrailViolations: string[];
  degraded: boolean;
  promptVersion: string;
  costUsd: number;
}
