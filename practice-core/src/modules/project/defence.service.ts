import { Inject, Injectable, Logger } from '@nestjs/common';
import type { Kysely } from 'kysely';
import { KYSELY } from '../../db/database.module';
import type { Database } from '../../db/schema';
import { appendTypedEvent } from '../event-store/attempt-event-type';
import { EventStoreRepository } from '../event-store/event-store.repository';
import { RubricRepository } from '../evaluation/rubric.repository';
import { AI_GRADER, type AiGrader } from '../evaluation/ai-grader.interface';
import { ProjectRepository } from './project.repository';
import { GitService } from './git.service';
import {
  VIVA_MODEL,
  type VivaModel,
  type VivaQuestion,
} from './viva-model.port';
import { projectError } from './project-error';

/**
 * Phase 3 (PLAN_PHASE3_PROJECTS.md 3.8 / B6). The defence viva:
 *
 *   - `startViva`: generates 6–8 grounded questions from the learner's
 *     OWN milestone-1 design doc + OWN commit history (via GitService,
 *     1.7) using VivaModel (a direct model call, D-P3-4 — not Phase 4's
 *     Mentor Service). Emits the first question as a DEFENCE_MESSAGE
 *     EXAMINER turn and stashes the rest.
 *   - `postMessage`: a learner turn; the service replies with the next
 *     stashed question (turn-by-turn). When the questions run out it
 *     closes the viva and runs the scorer.
 *   - `scoreViva`: renders the transcript, grades it against
 *     `rub.reasoning.v1` via the shared AI grader, records the score, and
 *     — because that rubric is ALWAYS_PROVISIONAL_UNTIL_CALIBRATED and
 *     the viva is certification-relevant — emits a
 *     DEFENCE_HUMAN_REVIEW_REQUIRED marker event (the admin/analytics
 *     layer builds the review queue from the event stream, same stance
 *     as sim tickets).
 *
 * Adversarial defence: the transcript (learner turns) is the only
 * untrusted input to the scorer; ClaudeAiGrader already delimits + flags
 * it (rule 35). The `scoreViva` result carries the grader's `provisional`
 * flag straight through.
 */

const VIVA_RUBRIC_ID = 'rub.reasoning.v1';
const DEFAULT_QUESTION_COUNT = 7;

interface VivaState {
  questions: VivaQuestion[];
  asked: number;
  closed: boolean;
}

export interface VivaScore {
  rubricId: string;
  overallLevel: number;
  criterionLevels: Record<string, number>;
  provisional: boolean;
  humanReviewRequired: boolean;
}

@Injectable()
export class DefenceService {
  private readonly logger = new Logger(DefenceService.name);
  // In-memory per-attempt question queue. A restart loses it; startViva
  // is idempotent-ish (regenerates), and the transcript itself is
  // durable in attempt_events so a resumed viva can be scored regardless.
  private readonly state = new Map<string, VivaState>();

  constructor(
    @Inject(KYSELY) private readonly db: Kysely<Database>,
    private readonly events: EventStoreRepository,
    private readonly projects: ProjectRepository,
    private readonly rubrics: RubricRepository,
    @Inject(AI_GRADER) private readonly grader: AiGrader,
    @Inject(VIVA_MODEL) private readonly model: VivaModel,
    private readonly git: GitService,
  ) {}

  async startViva(
    attemptId: string,
  ): Promise<{ turn: number; message: string; totalQuestions: number }> {
    await this.assertFinalReached(attemptId);

    // Assemble the grounding facts from the learner's own repo.
    const repoRef = await this.resolveRepoRef(attemptId);
    let designDoc = '';
    let commits: Array<{ sha: string; message: string }> = [];
    let diff: string | undefined;
    if (repoRef && this.git.isEnabled()) {
      designDoc =
        (await this.git.readFile(repoRef, 'DESIGN.md')) ??
        (await this.git.readFile(repoRef, 'docs/design.md')) ??
        '';
      const cs = await this.git.listCommits(repoRef, { limit: 50 });
      commits = cs.map((c) => ({ sha: c.sha, message: c.message }));
      if (commits.length >= 2) {
        diff =
          (await this.git.diff(
            repoRef,
            commits[commits.length - 1].sha,
            commits[0].sha,
          )) ?? undefined;
      }
    }

    let questions: VivaQuestion[];
    try {
      questions = await this.model.generateQuestions({
        designDoc,
        commits,
        diff,
        count: DEFAULT_QUESTION_COUNT,
      });
    } catch (e) {
      this.logger.error(
        `viva question generation failed for ${attemptId}: ${
          e instanceof Error ? e.message : String(e)
        }`,
      );
      questions = [
        {
          text: 'Walk me through the single most significant way your implementation diverged from your milestone-1 design, and why.',
          groundedIn: 'design',
          kind: 'divergence',
        },
      ];
    }

    this.state.set(attemptId, { questions, asked: 0, closed: false });
    const first = questions[0];
    const turn = await this.nextTurn(attemptId);
    await this.emitTurn(attemptId, 'EXAMINER', turn, first.text, {
      grounded_in: first.groundedIn,
      kind: first.kind,
      question_index: 1,
    });
    const st = this.state.get(attemptId)!;
    st.asked = 1;
    return { turn, message: first.text, totalQuestions: questions.length };
  }

  async postMessage(input: {
    attemptId: string;
    role: 'LEARNER' | 'EXAMINER';
    text: string;
  }): Promise<{
    turn: number;
    recorded: true;
    nextQuestion?: string;
    closed?: boolean;
    score?: VivaScore;
  }> {
    if (!input.text.trim()) {
      projectError('EMPTY_DEFENCE_MESSAGE', 'defence message text is required');
    }
    await this.assertFinalReached(input.attemptId);

    const turn = await this.nextTurn(input.attemptId);
    await this.emitTurn(input.attemptId, input.role, turn, input.text);

    // Only a learner turn advances the viva.
    if (input.role !== 'LEARNER') {
      return { turn, recorded: true };
    }
    const st = this.state.get(input.attemptId);
    if (!st || st.closed) {
      return { turn, recorded: true };
    }

    if (st.asked < st.questions.length) {
      const next = st.questions[st.asked];
      st.asked += 1;
      const qTurn = await this.nextTurn(input.attemptId);
      await this.emitTurn(input.attemptId, 'EXAMINER', qTurn, next.text, {
        grounded_in: next.groundedIn,
        kind: next.kind,
        question_index: st.asked,
      });
      return { turn, recorded: true, nextQuestion: next.text };
    }

    // out of questions → close + score
    st.closed = true;
    const score = await this.scoreViva(input.attemptId);
    return { turn, recorded: true, closed: true, score };
  }

  /**
   * Render the transcript and grade it against rub.reasoning.v1. Records
   * the score and, because the rubric is uncalibrated and the viva is
   * certification-relevant, emits a human-review-required marker.
   */
  async scoreViva(attemptId: string): Promise<VivaScore> {
    const rubric = this.rubrics.getRubric(VIVA_RUBRIC_ID);
    if (!rubric) {
      throw new Error(`viva rubric ${VIVA_RUBRIC_ID} not found`);
    }
    const transcript = await this.transcript(attemptId);
    const rendered = transcript
      .map((t) => `[turn ${t.turn}] ${t.role}: ${t.text}`)
      .join('\n');

    // Ground truth for the grader: the learner's own design + commits.
    const repoRef = await this.resolveRepoRef(attemptId);
    const commits =
      repoRef && this.git.isEnabled()
        ? (await this.git.listCommits(repoRef, { limit: 50 })).map(
            (c) => `${c.sha.slice(0, 8)} ${c.message.split('\n')[0]}`,
          )
        : [];

    const result = await this.grader.grade(rubric, {
      artifactText: rendered,
      appliedFaultIds: [],
      resolutionValidatorResults: [],
      commandSequence: commits,
    });

    const criterionLevels: Record<string, number> = {};
    for (const g of result.criterionGrades)
      criterionLevels[g.criterion] = g.level;
    const overallLevel =
      criterionLevels.overall ??
      Math.min(...result.criterionGrades.map((g) => g.level));

    const humanReviewRequired =
      result.provisional ||
      rubric.humanReviewPolicy === 'ALWAYS_PROVISIONAL_UNTIL_CALIBRATED';

    await appendTypedEvent(this.events, {
      attemptId,
      actor: 'AI',
      type: 'DEFENCE_MESSAGE',
      payload: {
        role: 'SYSTEM',
        turn: (await this.nextTurn(attemptId)) - 1,
        kind: 'score',
        rubric_id: VIVA_RUBRIC_ID,
        overall_level: overallLevel,
        criterion_levels: criterionLevels,
        provisional: result.provisional,
        human_review_required: humanReviewRequired,
      },
    });

    if (humanReviewRequired) {
      this.logger.log(
        `defence viva for attempt ${attemptId} scored overall=${overallLevel} (provisional) — queued for human review`,
      );
    }

    return {
      rubricId: VIVA_RUBRIC_ID,
      overallLevel,
      criterionLevels,
      provisional: result.provisional,
      humanReviewRequired,
    };
  }

  /** viva overall level 1..4 → [0,1] for sp.project.default's `defence` component (3.9). */
  async defenceComponentScore(attemptId: string): Promise<number | null> {
    const rows = await this.db
      .selectFrom('attempt.attempt_events')
      .select(['payload'])
      .where('attempt_id', '=', attemptId)
      .where('type', '=', 'DEFENCE_MESSAGE')
      .orderBy('seq', 'desc')
      .execute();
    for (const r of rows) {
      const p = r.payload as { kind?: string; overall_level?: number };
      if (p.kind === 'score' && typeof p.overall_level === 'number') {
        return clamp01((p.overall_level - 1) / 3);
      }
    }
    return null;
  }

  /** Full transcript, oldest turn first (message turns only, not the score marker). */
  async transcript(
    attemptId: string,
  ): Promise<Array<{ turn: number; role: string; text: string }>> {
    const rows = await this.db
      .selectFrom('attempt.attempt_events')
      .select(['payload'])
      .where('attempt_id', '=', attemptId)
      .where('type', '=', 'DEFENCE_MESSAGE')
      .orderBy('seq', 'asc')
      .execute();
    return rows
      .map(
        (r) =>
          r.payload as {
            turn?: number;
            role?: string;
            text?: string;
            kind?: string;
          },
      )
      .filter((p) => p.kind !== 'score' && typeof p.text === 'string')
      .map((p) => ({
        turn: p.turn ?? 0,
        role: p.role ?? 'UNKNOWN',
        text: p.text ?? '',
      }));
  }

  // --- helpers ---------------------------------------------------------

  private async assertFinalReached(attemptId: string): Promise<void> {
    const finalMs = await this.projects.getMilestone(attemptId, 'final');
    if (!finalMs || finalMs.status === 'LOCKED') {
      projectError(
        'DEFENCE_NOT_AVAILABLE',
        'the defence viva opens when the `final` milestone is reached',
      );
    }
  }

  private async emitTurn(
    attemptId: string,
    role: 'LEARNER' | 'EXAMINER' | 'SYSTEM',
    turn: number,
    text: string,
    extra?: Record<string, unknown>,
  ): Promise<void> {
    await appendTypedEvent(this.events, {
      attemptId,
      actor: role === 'LEARNER' ? 'LEARNER' : 'AI',
      type: 'DEFENCE_MESSAGE',
      payload: { role, turn, text, ...(extra ?? {}) },
    });
  }

  private async nextTurn(attemptId: string): Promise<number> {
    const rows = await this.db
      .selectFrom('attempt.attempt_events')
      .select(['payload'])
      .where('attempt_id', '=', attemptId)
      .where('type', '=', 'DEFENCE_MESSAGE')
      .execute();
    return rows.length + 1;
  }

  private async resolveRepoRef(attemptId: string): Promise<string | null> {
    const subs = await this.projects.listSubmissions(attemptId);
    return (
      subs.find((s) => s.repo_ref && s.repo_ref !== 'none')?.repo_ref ?? null
    );
  }
}

function clamp01(n: number): number {
  return Math.max(0, Math.min(1, Number.isFinite(n) ? n : 0));
}
