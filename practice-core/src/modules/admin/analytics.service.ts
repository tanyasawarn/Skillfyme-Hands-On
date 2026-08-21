import { Inject, Injectable } from '@nestjs/common';
import { sql, type Kysely } from 'kysely';
import { KYSELY } from '../../db/database.module';
import type { Database } from '../../db/schema';

export interface ActivityHealthRow {
  activity_id: string;
  slug: string;
  attempts_started: number;
  attempts_completed: number;
  attempts_abandoned: number;
  pass_rate: number | null;
  avg_hint_penalty: number | null;
  avg_reset_count: number | null;
}

export interface TaskDropoffRow {
  task_key: string;
  attempts_reached: number;
  attempts_passed: number;
  dropoff_rate: number;
}

/**
 * Doc §11.3 admin analytics table, Phase 1 subset per §13.1 ("basic admin
 * analytics... Postgres-read-replica-backed, no ClickHouse yet" -- this
 * project has no read replica yet either, so these queries run against
 * the primary; the doc's ClickHouse migration is explicitly Phase 3+).
 * Every query here is a plain aggregate over attempt/attempt_score/
 * attempt_task_state -- no materialised views yet (doc §8.4 recommends
 * mv_activity_health for this at scale; Phase 1's attempt volume doesn't
 * need it).
 */
@Injectable()
export class AnalyticsService {
  constructor(@Inject(KYSELY) private readonly db: Kysely<Database>) {}

  /** Doc §11.3: "Attempts started/completed/abandoned per activity... Pass rate on first attempt." */
  async getActivityHealth(tenantId: string): Promise<ActivityHealthRow[]> {
    const rows = await sql<{
      activity_id: string;
      slug: string;
      attempts_started: string;
      attempts_completed: string;
      attempts_abandoned: string;
      passed_count: string;
      scored_count: string;
      avg_hint_penalty: string | null;
      avg_reset_count: string | null;
    }>`
      SELECT
        a.id AS activity_id,
        a.slug,
        count(att.id) AS attempts_started,
        count(att.id) FILTER (WHERE att.status IN ('PASSED', 'FAILED', 'COMPLETED')) AS attempts_completed,
        count(att.id) FILTER (WHERE att.status IN ('ABANDONED', 'EXPIRED')) AS attempts_abandoned,
        count(att.id) FILTER (WHERE att.status IN ('PASSED', 'COMPLETED')) AS passed_count,
        count(att.id) FILTER (WHERE att.status IN ('PASSED', 'FAILED', 'COMPLETED')) AS scored_count,
        avg(att.hint_penalty_total) AS avg_hint_penalty,
        avg(att.reset_count) AS avg_reset_count
      FROM content.activity a
      LEFT JOIN attempt.attempt att ON att.activity_id = a.id
      WHERE a.tenant_id = ${tenantId}
      GROUP BY a.id, a.slug
      ORDER BY attempts_started DESC
    `.execute(this.db);

    return rows.rows.map((r) => ({
      activity_id: r.activity_id,
      slug: r.slug,
      attempts_started: Number(r.attempts_started),
      attempts_completed: Number(r.attempts_completed),
      attempts_abandoned: Number(r.attempts_abandoned),
      pass_rate:
        Number(r.scored_count) > 0
          ? Number(r.passed_count) / Number(r.scored_count)
          : null,
      avg_hint_penalty:
        r.avg_hint_penalty !== null ? Number(r.avg_hint_penalty) : null,
      avg_reset_count:
        r.avg_reset_count !== null ? Number(r.avg_reset_count) : null,
    }));
  }

  /**
   * Doc §11.3: "Drop-off by task (which task loses learners) -- The
   * single best content-improvement signal." Rate is computed against
   * attempts that reached the *previous* task (or all attempts, for the
   * first task), not against total attempts overall -- otherwise a
   * multi-task activity's later tasks would look artificially healthy
   * just because fewer learners ever reach them for unrelated reasons.
   */
  async getTaskDropoff(activityId: string): Promise<TaskDropoffRow[]> {
    const rows = await sql<{
      task_key: string;
      reached: string;
      passed: string;
    }>`
      SELECT
        ats.task_key,
        count(*) AS reached,
        count(*) FILTER (WHERE ats.status = 'PASSED') AS passed
      FROM attempt.attempt_task_state ats
      JOIN attempt.attempt att ON att.id = ats.attempt_id
      WHERE att.activity_id = ${activityId}
      GROUP BY ats.task_key
      ORDER BY ats.task_key
    `.execute(this.db);

    return rows.rows.map((r) => ({
      task_key: r.task_key,
      attempts_reached: Number(r.reached),
      attempts_passed: Number(r.passed),
      dropoff_rate:
        Number(r.reached) > 0 ? 1 - Number(r.passed) / Number(r.reached) : 0,
    }));
  }

  /** Doc §11.3: "Validator failure frequency + ERROR rate -- Flaky or badly-authored validators." */
  async getValidatorHealth(activityId: string) {
    const rows = await sql<{
      validator_id: string;
      total: string;
      failed: string;
      errored: string;
    }>`
      SELECT
        vr.validator_id,
        count(*) AS total,
        count(*) FILTER (WHERE vr.status = 'FAIL') AS failed,
        count(*) FILTER (WHERE vr.status = 'ERROR') AS errored
      FROM attempt.validator_result vr
      JOIN attempt.validation_run run ON run.id = vr.validation_run_id
      JOIN attempt.attempt att ON att.id = run.attempt_id
      WHERE att.activity_id = ${activityId}
      GROUP BY vr.validator_id
      ORDER BY vr.validator_id
    `.execute(this.db);

    return rows.rows.map((r) => ({
      validator_id: r.validator_id,
      total_runs: Number(r.total),
      fail_rate: Number(r.total) > 0 ? Number(r.failed) / Number(r.total) : 0,
      error_rate: Number(r.total) > 0 ? Number(r.errored) / Number(r.total) : 0,
    }));
  }
}
