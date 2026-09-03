import { Inject, Injectable, Logger } from '@nestjs/common';
import { Kysely } from 'kysely';
import { KYSELY } from '../../db/database.module';
import type { ActivityMode, Database } from '../../db/schema';
import { LlmGatewayService } from '../llm-gateway/llm-gateway.service';
import { SpecLintService } from '../content-ci/spec-lint.service';

/**
 * PLAN.md G12 / doc §3.1 point 10: "AI-assisted authoring -- generate
 * first drafts of tasks, hints, distractors and validator scaffolds from
 * a topic + solution repo. Human approves. This is the single
 * highest-ROI internal AI use case."
 *
 * draft():
 *   1. builds an authoring prompt from a topic + target skills + mode +
 *      any solution-repo notes the author pastes in.
 *   2. routes through the LLM Gateway (taskClass 'authoring' -> strong
 *      tier, doc §7.6) -- so authoring shares the platform's routing /
 *      caching / budgeting / redaction / observability chokepoint.
 *   3. extracts the YAML block, parses it, and LINTS it against
 *      contracts/activity_spec.schema.json (reusing SpecLintService --
 *      the exact same check content CI runs).
 *   4. returns { draftYaml, parsed, valid, lintErrors } for HUMAN
 *      APPROVAL. It NEVER publishes -- the author takes the draft into
 *      the repo / CMS draft flow (§3.7, admin module).
 *
 * Internal tool: no learner-facing route; wired under /v1/admin.
 */
@Injectable()
export class AuthoringAssistantService {
  private readonly logger = new Logger(AuthoringAssistantService.name);

  constructor(
    @Inject(KYSELY) private readonly db: Kysely<Database>,
    private readonly gateway: LlmGatewayService,
    private readonly lint: SpecLintService,
  ) {}

  async draft(input: DraftInput): Promise<DraftResult> {
    const skills = input.skillSlugs ?? [];
    const knownSkills = await this.resolveSkills(skills);

    const system = [
      'You are an internal content-authoring assistant for a hands-on',
      'practice platform. Produce a FIRST DRAFT of an activity spec in',
      'YAML that conforms to contracts/activity_spec.schema.json. A human',
      'editor will review, fix, and publish it -- your job is to save',
      'them the blank-page cost, not to be final.',
      '',
      'Rules:',
      '- Output ONE fenced ```yaml block and nothing else.',
      '- Include: id, version:1, mode, status: DRAFT, meta',
      '  (difficulty_level, estimated_minutes), curriculum, skills',
      '  (with one primary), environment (blueprint, cost_budget_usd),',
      '  tasks (each with key, title, instructions_md, validators, hints),',
      '  completion, scoring.',
      '- Validators: use the typed catalogue (SHELL_ASSERT, SHELL_JSON,',
      '  FILE_EXISTS, FILE_CONTENT, FILE_PARSE, K8S_ASSERT). Every',
      '  validator needs an authored on_fail message.',
      '- Hints: a 3-level ladder, increasing directness, with penalties.',
      '- Do NOT invent skill slugs -- use only: ' + skills.join(', '),
    ].join('\n');

    const user = [
      `Topic: ${input.topic}`,
      `Mode: ${input.mode}`,
      input.solutionRepoNotes
        ? `Solution-repo notes (what a correct solution does):\n<untrusted-learner-content>${input.solutionRepoNotes}</untrusted-learner-content>`
        : '',
      'Draft the activity spec now.',
    ]
      .filter(Boolean)
      .join('\n\n');

    const res = await this.gateway.call({
      taskClass: 'authoring',
      promptVersion: 'authoring.system.v1',
      system,
      user,
      // authoring is an internal action -- charge a generous, separate
      // budget scope by leaving attempt/user unset (global only).
      maxOutputTokens: 2048,
    });

    if (res.degraded) {
      return {
        draftYaml: '',
        parsed: null,
        valid: false,
        lintErrors: [`llm gateway degraded: ${res.degraded}`],
        costUsd: res.costUsd,
      };
    }

    const yamlBlock = this.extractYaml(res.text);
    if (!yamlBlock) {
      return {
        draftYaml: res.text,
        parsed: null,
        valid: false,
        lintErrors: ['model did not return a ```yaml block'],
        costUsd: res.costUsd,
      };
    }

    let parsed: unknown;
    try {
      parsed = this.lint.parseYaml(yamlBlock);
    } catch (e) {
      return {
        draftYaml: yamlBlock,
        parsed: null,
        valid: false,
        lintErrors: [`YAML parse error: ${(e as Error).message}`],
        costUsd: res.costUsd,
      };
    }

    const lintResult = this.lint.lint(parsed, knownSkills);
    return {
      draftYaml: yamlBlock,
      parsed,
      valid: lintResult.valid,
      lintErrors: lintResult.issues.map((i) => `${i.path}: ${i.message}`),
      costUsd: res.costUsd,
    };
  }

  private extractYaml(text: string): string | null {
    const m = text.match(/```(?:yaml|yml)?\s*\n([\s\S]*?)```/);
    return m ? m[1].trim() : null;
  }

  private async resolveSkills(slugs: string[]): Promise<Set<string>> {
    if (slugs.length === 0) {
      const all = await this.db
        .selectFrom('skill.skill')
        .select('slug')
        .execute();
      return new Set(all.map((r) => r.slug));
    }
    const rows = await this.db
      .selectFrom('skill.skill')
      .select('slug')
      .where('slug', 'in', slugs)
      .execute();
    return new Set(rows.map((r) => r.slug));
  }
}

export interface DraftInput {
  topic: string;
  mode: ActivityMode;
  skillSlugs?: string[];
  solutionRepoNotes?: string;
}

export interface DraftResult {
  draftYaml: string;
  parsed: unknown;
  valid: boolean;
  lintErrors: string[];
  costUsd: number;
}
