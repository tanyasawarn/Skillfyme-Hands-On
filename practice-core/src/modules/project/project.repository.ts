import { Injectable } from '@nestjs/common';
import { sql } from 'kysely';
import { BaseRepository } from '../../common/base.repository';
import type {
  ProjectMilestoneKey,
  ProjectMilestoneStatus,
  ProjectSubmissionOutcome,
} from '../../db/schema';

/**
 * Phase 3 (PLAN_PHASE3_PROJECTS.md 0.10 / B1). Data access for the two
 * project-mode tables (db/migrations/0010_project_mode.sql). This is the
 * repository layer only -- the state machine that decides WHEN to move a
 * milestone from LOCKED->OPEN->SUBMITTED->GATED_* is Stage 1.6
 * (ProjectService). Kept deliberately thin and mechanical, same shape as
 * the other Kysely repositories in this codebase (extends BaseRepository
 * for the shared `@Inject(KYSELY)` constructor -- PLAN.md U6).
 */

export interface MilestoneStateRow {
  attempt_id: string;
  milestone_key: ProjectMilestoneKey;
  status: ProjectMilestoneStatus;
  ordinal: number;
  submitted_at: Date | null;
  gated_at: Date | null;
  score: number | null;
  rubric_level: number | null;
  attempt_count: number;
}

export interface SeedMilestoneInput {
  milestoneKey: ProjectMilestoneKey;
  ordinal: number;
  /** first milestone (ordinal 0) starts OPEN; the rest start LOCKED. */
  status: ProjectMilestoneStatus;
}

export interface RecordSubmissionInput {
  attemptId: string;
  milestoneKey: ProjectMilestoneKey;
  repoRef: string;
  commitSha?: string;
  attemptNumber: number;
}

export interface GateOutcomeInput {
  attemptId: string;
  milestoneKey: ProjectMilestoneKey;
  outcome: ProjectSubmissionOutcome;
  score: number;
  rubricLevel?: number;
}

export interface SubmissionRow {
  id: string;
  attempt_id: string;
  milestone_key: ProjectMilestoneKey;
  repo_ref: string;
  commit_sha: string;
  attempt_number: number;
  outcome: ProjectSubmissionOutcome | null;
  score: number | null;
  submitted_at: Date;
}

@Injectable()
export class ProjectRepository extends BaseRepository {
  /**
   * Create the milestone rows for a fresh project attempt, in one
   * statement. Idempotent per (attempt_id, milestone_key) via ON
   * CONFLICT DO NOTHING -- re-seeding an already-seeded attempt is a
   * no-op, never a duplicate-key error.
   */
  async seedMilestones(
    attemptId: string,
    milestones: SeedMilestoneInput[],
  ): Promise<void> {
    if (milestones.length === 0) return;
    await this.db
      .insertInto('attempt.project_milestone_state')
      .values(
        milestones.map((m) => ({
          attempt_id: attemptId,
          milestone_key: m.milestoneKey,
          status: m.status,
          ordinal: m.ordinal,
        })),
      )
      .onConflict((oc) =>
        oc.columns(['attempt_id', 'milestone_key']).doNothing(),
      )
      .execute();
  }

  async listMilestones(attemptId: string): Promise<MilestoneStateRow[]> {
    return this.db
      .selectFrom('attempt.project_milestone_state')
      .select([
        'attempt_id',
        'milestone_key',
        'status',
        'ordinal',
        'submitted_at',
        'gated_at',
        'score',
        'rubric_level',
        'attempt_count',
      ])
      .where('attempt_id', '=', attemptId)
      .orderBy('ordinal', 'asc')
      .execute();
  }

  async getMilestone(
    attemptId: string,
    milestoneKey: ProjectMilestoneKey,
  ): Promise<MilestoneStateRow | undefined> {
    return this.db
      .selectFrom('attempt.project_milestone_state')
      .select([
        'attempt_id',
        'milestone_key',
        'status',
        'ordinal',
        'submitted_at',
        'gated_at',
        'score',
        'rubric_level',
        'attempt_count',
      ])
      .where('attempt_id', '=', attemptId)
      .where('milestone_key', '=', milestoneKey)
      .executeTakeFirst();
  }

  /**
   * Mark a milestone SUBMITTED and bump attempt_count. Returns the new
   * attempt_count so the caller can pass it to recordSubmission().
   */
  async markSubmitted(
    attemptId: string,
    milestoneKey: ProjectMilestoneKey,
  ): Promise<number> {
    const row = await this.db
      .updateTable('attempt.project_milestone_state')
      .set({
        status: 'SUBMITTED',
        submitted_at: sql`now()`,
        attempt_count: sql`attempt_count + 1`,
        updated_at: sql`now()`,
      })
      .where('attempt_id', '=', attemptId)
      .where('milestone_key', '=', milestoneKey)
      .returning('attempt_count')
      .executeTakeFirstOrThrow();
    return row.attempt_count;
  }

  /**
   * Apply a gate outcome to a milestone. On GATED_PASS this also opens
   * the next milestone (the immediately-following ordinal) if it is
   * currently LOCKED -- one statement, no read-then-write race.
   */
  async applyGateOutcome(input: GateOutcomeInput): Promise<void> {
    const status: ProjectMilestoneStatus =
      input.outcome === 'GATED_PASS' ? 'GATED_PASS' : 'GATED_FAIL';

    await this.db.transaction().execute(async (trx) => {
      const gated = await trx
        .updateTable('attempt.project_milestone_state')
        .set({
          status,
          score: input.score,
          rubric_level: input.rubricLevel ?? null,
          gated_at: sql`now()`,
          updated_at: sql`now()`,
        })
        .where('attempt_id', '=', input.attemptId)
        .where('milestone_key', '=', input.milestoneKey)
        .returning('ordinal')
        .executeTakeFirstOrThrow();

      if (input.outcome === 'GATED_PASS') {
        await trx
          .updateTable('attempt.project_milestone_state')
          .set({ status: 'OPEN', updated_at: sql`now()` })
          .where('attempt_id', '=', input.attemptId)
          .where('ordinal', '=', gated.ordinal + 1)
          .where('status', '=', 'LOCKED')
          .execute();
      }
    });
  }

  async recordSubmission(input: RecordSubmissionInput): Promise<SubmissionRow> {
    return this.db
      .insertInto('attempt.project_submission')
      .values({
        attempt_id: input.attemptId,
        milestone_key: input.milestoneKey,
        repo_ref: input.repoRef,
        commit_sha: input.commitSha ?? '',
        attempt_number: input.attemptNumber,
      })
      .returningAll()
      .executeTakeFirstOrThrow() as unknown as Promise<SubmissionRow>;
  }

  /**
   * Stamp the gate outcome + score onto the most recent submission row
   * for a milestone (append-only history stays intact; we only fill in
   * the outcome of the row that was just graded).
   */
  async stampSubmissionOutcome(
    attemptId: string,
    milestoneKey: ProjectMilestoneKey,
    attemptNumber: number,
    outcome: ProjectSubmissionOutcome,
    score: number,
  ): Promise<void> {
    await this.db
      .updateTable('attempt.project_submission')
      .set({ outcome, score })
      .where('attempt_id', '=', attemptId)
      .where('milestone_key', '=', milestoneKey)
      .where('attempt_number', '=', attemptNumber)
      .execute();
  }

  async listSubmissions(
    attemptId: string,
    milestoneKey?: ProjectMilestoneKey,
  ): Promise<SubmissionRow[]> {
    let q = this.db
      .selectFrom('attempt.project_submission')
      .selectAll()
      .where('attempt_id', '=', attemptId);
    if (milestoneKey) q = q.where('milestone_key', '=', milestoneKey);
    return q.orderBy('submitted_at', 'desc').execute() as unknown as Promise<
      SubmissionRow[]
    >;
  }
}
