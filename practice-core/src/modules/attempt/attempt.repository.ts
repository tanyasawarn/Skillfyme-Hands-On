import {
  Injectable,
  ConflictException,
  Inject,
  Optional,
} from '@nestjs/common';
import { sql, type Kysely } from 'kysely';
import type { AttemptStatus, Database } from '../../db/schema';
import { AttemptStatusGroups } from '../../common/attempt-status-groups';
import { BaseRepository } from '../../common/base.repository';
import { KYSELY } from '../../db/database.module';
import { MetricsService } from '../metrics/metrics.service';

@Injectable()
export class AttemptRepository extends BaseRepository {
  /**
   * MetricsService is @Optional so this repository stays constructible
   * as `new AttemptRepository(db)` in unit tests that don't care about
   * metrics -- the real app always provides it (MetricsModule is
   * @Global). transition() is the single chokepoint every attempt
   * state change passes through, so instrumenting here (rather than at
   * all 8 call sites in attempt.service.ts) is both complete and
   * minimal.
   */
  constructor(
    @Inject(KYSELY) db: Kysely<Database>,
    @Optional() private readonly metrics?: MetricsService,
  ) {
    super(db);
  }

  async create(input: {
    tenantId: string;
    userId: string;
    activityId: string;
    activityVersionId: string;
    mode: string;
    idempotencyKey?: string;
    retryOfAttemptId?: string;
    retryIndex?: number;
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
        ...(input.retryOfAttemptId
          ? { retry_of_attempt_id: input.retryOfAttemptId }
          : {}),
        ...(input.retryIndex !== undefined
          ? { retry_index: input.retryIndex }
          : {}),
      })
      .returningAll()
      .executeTakeFirstOrThrow();
  }

  /**
   * Doc §4.5 / §2.7: "A retry is a new Attempt row with retry_of_attempt_id
   * and incremented retry_index." Finds the most recent attempt this
   * learner has on the same activity (any version -- learner_activity_state
   * itself is keyed by activity_id, not activity_version_id, so retry
   * tracking uses the same granularity) so createAttempt can chain a new
   * attempt onto it. Only a completed attempt counts as something to
   * retry from -- an attempt still in flight isn't a prior try yet.
   *
   * PLAN.md K7: this status list used to be hand-copied here separately
   * from AttemptService.TERMINAL_STATUSES and had silently drifted to
   * omit PROVISION_FAILED -- an attempt that failed to provision was not
   * treated as "completed" for retry-chaining, with no documented reason
   * for the difference. Now derived from the same shared
   * AttemptStatusGroups.RETRYABLE_FROM every other terminal-status check
   * uses.
   */
  async findMostRecentCompletedAttempt(userId: string, activityId: string) {
    return this.db
      .selectFrom('attempt.attempt')
      .selectAll()
      .where('user_id', '=', userId)
      .where('activity_id', '=', activityId)
      .where('status', 'in', [...AttemptStatusGroups.RETRYABLE_FROM])
      .orderBy('created_at', 'desc')
      .executeTakeFirst();
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
      snapshotId: string | null;
      snapshotTakenAt: Date | null;
    }>,
  ) {
    const result = await this.db
      .updateTable('attempt.attempt')
      .set((eb) => ({
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
        ...(updates.snapshotId !== undefined
          ? { snapshot_id: updates.snapshotId }
          : {}),
        ...(updates.snapshotTakenAt !== undefined
          ? { snapshot_taken_at: updates.snapshotTakenAt }
          : {}),
        // Any state transition is, by definition, activity -- keeps the
        // 72h cache sweep's clock honest without every call site having
        // to remember to bump it separately.
        last_activity_at: eb.fn('now', []),
        version: expectedVersion + 1,
      }))
      .where('id', '=', attemptId)
      .where('version', '=', expectedVersion)
      .returningAll()
      .executeTakeFirst();

    if (!result) {
      throw new ConflictException(
        `attempt ${attemptId} was modified concurrently (expected version ${expectedVersion}) -- doc §4.4 optimistic concurrency`,
      );
    }
    if (updates.status) {
      this.metrics?.recordAttemptTransition(updates.status);
    }
    return result;
  }

  /**
   * Bumps last_activity_at without a status transition -- for
   * learner-driven actions that prove the attempt is still being actively
   * used (connect, file read/write, hint reveal) but aren't themselves a
   * state-machine step. No version check: this is a liveness signal, not
   * a state mutation, so it can't conflict with a concurrent transition
   * in any way that matters.
   */
  async touch(attemptId: string): Promise<void> {
    await this.db
      .updateTable('attempt.attempt')
      .set((eb) => ({ last_activity_at: eb.fn('now', []) }))
      .where('id', '=', attemptId)
      .execute();
  }

  /**
   * The statuses a "live" (compute-consuming, non-terminal) attempt can be
   * in -- CREATED/PROVISIONING/READY/IN_PROGRESS are what "always visible
   * in history" (revised lifecycle requirement §6) refers to, and were
   * the direct-to-CACHED sweep target before the two-stage revision
   * below. Kept as a named group (dashboard.controller.ts and elsewhere
   * still care about "which statuses count as an active lab"), but no
   * longer what CacheSweepService sweeps from -- see
   * findStaleSuspendedAttempts.
   */
  static readonly LIVE_CACHEABLE_STATUSES: readonly AttemptStatus[] = [
    'CREATED',
    'PROVISIONING',
    'READY',
    'IN_PROGRESS',
  ];

  /**
   * Revised lifecycle requirement §3/§7: the 15-minute inactivity ->
   * SUSPENDED transition already happens end-to-end via the Go
   * orchestrator's idle detector (real compute teardown) publishing
   * ENV_DESTROYED(reason="idle"), consumed by
   * AttemptService.handleEnvironmentDestroyed. SUSPENDED already has zero
   * backend cost (§4) -- CACHED is a *second*, later stage for attempts
   * nobody ever returns to (history/cleanup, not cost control, since
   * SUSPENDED already achieved that). This sweep therefore only reads
   * from SUSPENDED, never from a live status directly -- an attempt must
   * pass through SUSPENDED first, matching the diagram in the
   * requirement (active -> suspended -> cached).
   */
  async findStaleSuspendedAttempts(olderThan: Date) {
    return this.db
      .selectFrom('attempt.attempt')
      .selectAll()
      .where('status', '=', 'SUSPENDED')
      .where(sql<boolean>`last_activity_at < ${olderThan}`)
      .execute();
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
