import { Inject, Injectable, ConflictException } from '@nestjs/common';
import type { Kysely } from 'kysely';
import { KYSELY } from '../../db/database.module';
import type { AttemptStatus, Database } from '../../db/schema';

@Injectable()
export class AttemptRepository {
  constructor(@Inject(KYSELY) private readonly db: Kysely<Database>) {}

  async create(input: {
    tenantId: string;
    userId: string;
    activityId: string;
    activityVersionId: string;
    mode: string;
    idempotencyKey?: string;
  }) {
    if (input.idempotencyKey) {
      const existing = await this.db
        .selectFrom('attempt.attempt')
        .selectAll()
        .where('user_id', '=', input.userId)
        .where('activity_version_id', '=', input.activityVersionId)
        .where('idempotency_key', '=', input.idempotencyKey)
        .executeTakeFirst();
      // doc §4.4: "Attempt operations are idempotent -- every mutating
      // endpoint takes an Idempotency-Key; duplicate submits are no-ops."
      if (existing) return existing;
    }

    return this.db
      .insertInto('attempt.attempt')
      .values({
        tenant_id: input.tenantId,
        user_id: input.userId,
        activity_id: input.activityId,
        activity_version_id: input.activityVersionId,
        mode: input.mode,
        status: 'CREATED',
        ...(input.idempotencyKey
          ? { idempotency_key: input.idempotencyKey }
          : {}),
      })
      .returningAll()
      .executeTakeFirstOrThrow();
  }

  async findById(id: string) {
    return this.db
      .selectFrom('attempt.attempt')
      .selectAll()
      .where('id', '=', id)
      .executeTakeFirst();
  }

  /**
   * Doc §4.4: "Optimistic concurrency on attempt state via a version
   * column; the orchestrator and the API both write, so conflicts are
   * real." Every transition checks-and-increments version; a mismatch
   * throws so the caller can retry rather than silently clobbering a
   * concurrent writer (e.g. the reaper force-destroying while the API
   * is also processing a submit).
   */
  async transition(
    attemptId: string,
    expectedVersion: number,
    updates: Partial<{
      status: AttemptStatus;
      environmentId: string | null;
      startedAt: Date;
      submittedAt: Date;
      completedAt: Date;
      expiresAt: Date | null;
    }>,
  ) {
    const result = await this.db
      .updateTable('attempt.attempt')
      .set({
        ...(updates.status ? { status: updates.status } : {}),
        ...(updates.environmentId !== undefined
          ? { environment_id: updates.environmentId }
          : {}),
        ...(updates.startedAt ? { started_at: updates.startedAt } : {}),
        ...(updates.submittedAt ? { submitted_at: updates.submittedAt } : {}),
        ...(updates.completedAt ? { completed_at: updates.completedAt } : {}),
        ...(updates.expiresAt !== undefined
          ? { expires_at: updates.expiresAt }
          : {}),
        version: expectedVersion + 1,
      })
      .where('id', '=', attemptId)
      .where('version', '=', expectedVersion)
      .returningAll()
      .executeTakeFirst();

    if (!result) {
      throw new ConflictException(
        `attempt ${attemptId} was modified concurrently (expected version ${expectedVersion}) -- doc §4.4 optimistic concurrency`,
      );
    }
    return result;
  }

  async listForUser(userId: string, status?: AttemptStatus[]) {
    let query = this.db
      .selectFrom('attempt.attempt')
      .selectAll()
      .where('user_id', '=', userId)
      .orderBy('created_at', 'desc');
    if (status && status.length > 0) {
      query = query.where('status', 'in', status);
    }
    return query.execute();
  }

  /** Doc §8.3: GET /v1/practice/attempts/{id}/evaluation -- latest score, most recent first. */
  async getLatestScore(attemptId: string) {
    return this.db
      .selectFrom('attempt.attempt_score')
      .selectAll()
      .where('attempt_id', '=', attemptId)
      .orderBy('computed_at', 'desc')
      .executeTakeFirst();
  }

  /** Doc §8.3: GET /v1/practice/attempts/{id}/tasks -- what the workspace task checklist reads. */
  async getTaskStates(attemptId: string) {
    return this.db
      .selectFrom('attempt.attempt_task_state')
      .selectAll()
      .where('attempt_id', '=', attemptId)
      .execute();
  }
}
