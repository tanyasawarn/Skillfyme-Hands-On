import { Inject, Injectable } from '@nestjs/common';
import { sql, type Kysely } from 'kysely';
import { KYSELY } from '../../db/database.module';
import type { Database } from '../../db/schema';

/**
 * Phase 3 (PLAN_PHASE3_PROJECTS.md 4.3 / B10). Admin cost dashboard API.
 *
 * Blended cost per learner / course / activity / tenant, from:
 *   - env.usage_meter.total_cost_usd  (compute + AI, Phase 1)
 *   - env.usage_meter.cloud_cost_usd  (T3 cloud spend, Stage 2.5)
 *   - env.cloud_account lifecycle     (per-account spend + quarantine flags, Stage 2.4)
 *
 * env.* is Dev A's schema (not in the Kysely Database type), so every
 * query is raw `sql`. The dashboard grain is one of learner / course /
 * activity / tenant; a "per-account" view lists each vended account with
 * its spend and whether it's quarantined.
 *
 * Degrades gracefully: if the env schema isn't present (a practice-core
 * running without the orchestrator's migrations), the queries return
 * empty rather than throwing — checked once via `hasEnvSchema()`.
 */
export type CostGrain = 'learner' | 'course' | 'activity' | 'tenant';

export interface CostRow {
  bucket: string; // the grain key
  label: string; // human-readable (slug / email / title) where resolvable
  attempts: number;
  compute_cost_usd: number;
  cloud_cost_usd: number;
  ai_cost_usd: number;
  total_cost_usd: number;
}

export interface AccountSpendRow {
  aws_account_id: string;
  state: string;
  region: string;
  attempt_id: string | null;
  budget_usd: number | null;
  spend_usd: number;
  quarantined: boolean;
  quarantine_reason: string | null;
}

export interface BudgetBreachRow {
  aws_account_id: string;
  attempt_id: string | null;
  reason: string;
  resources_remaining: number | null;
  at: string;
}

@Injectable()
export class CostDashboardService {
  private envSchemaPresent: boolean | null = null;

  constructor(@Inject(KYSELY) private readonly db: Kysely<Database>) {}

  private async hasEnvSchema(): Promise<boolean> {
    if (this.envSchemaPresent !== null) return this.envSchemaPresent;
    const res = await sql<{ present: boolean }>`
      SELECT EXISTS (
        SELECT 1 FROM information_schema.tables
         WHERE table_schema = 'env' AND table_name = 'usage_meter'
      ) AS present
    `.execute(this.db);
    this.envSchemaPresent = Boolean(res.rows[0]?.present);
    return this.envSchemaPresent;
  }

  /**
   * Blended cost grouped at `grain` for a tenant, over the last `days`.
   * `usage_meter.attempt_id` is text; joins to attempt on a cast.
   */
  async costByGrain(
    tenantId: string,
    grain: CostGrain,
    days = 30,
  ): Promise<CostRow[]> {
    if (!(await this.hasEnvSchema())) return [];

    const bucket =
      grain === 'learner'
        ? sql`u.email`
        : grain === 'course'
          ? sql`COALESCE(c.slug, 'no-course')`
          : grain === 'activity'
            ? sql`act.slug`
            : sql`t.name`;
    const key =
      grain === 'learner'
        ? sql`a.user_id::text`
        : grain === 'course'
          ? sql`COALESCE(c.id::text, '')`
          : grain === 'activity'
            ? sql`a.activity_id::text`
            : sql`a.tenant_id::text`;

    const res = await sql<{
      bucket: string;
      label: string;
      attempts: string;
      compute_cost_usd: string;
      cloud_cost_usd: string;
      ai_cost_usd: string;
      total_cost_usd: string;
    }>`
      SELECT ${key}   AS bucket,
             ${bucket} AS label,
             count(DISTINCT a.id)                                             AS attempts,
             COALESCE(SUM(um.total_cost_usd - um.cloud_cost_usd - um.ai_cost_usd), 0) AS compute_cost_usd,
             COALESCE(SUM(um.cloud_cost_usd), 0)                              AS cloud_cost_usd,
             COALESCE(SUM(um.ai_cost_usd), 0)                                 AS ai_cost_usd,
             COALESCE(SUM(um.total_cost_usd), 0)                              AS total_cost_usd
        FROM env.usage_meter um
        JOIN attempt.attempt a ON a.id = um.attempt_id::uuid
        JOIN learner.user_account u ON u.id = a.user_id
        JOIN learner.tenant t ON t.id = a.tenant_id
        JOIN content.activity act ON act.id = a.activity_id
        LEFT JOIN LATERAL (
          SELECT m.course_id
            FROM content.activity_topic at2
            JOIN content.topic tp ON tp.id = at2.topic_id
            JOIN content.module m ON m.id = tp.module_id
           WHERE at2.activity_version_id = a.activity_version_id
           LIMIT 1
        ) mod ON true
        LEFT JOIN content.course c ON c.id = mod.course_id
       WHERE a.tenant_id = ${tenantId}::uuid
         AND um.window_start >= now() - make_interval(days => ${Math.max(1, Math.floor(days))})
       GROUP BY 1, 2
       ORDER BY total_cost_usd DESC
    `.execute(this.db);

    return res.rows.map((r) => ({
      bucket: r.bucket,
      label: r.label,
      attempts: Number(r.attempts),
      compute_cost_usd: round4(Number(r.compute_cost_usd)),
      cloud_cost_usd: round4(Number(r.cloud_cost_usd)),
      ai_cost_usd: round4(Number(r.ai_cost_usd)),
      total_cost_usd: round4(Number(r.total_cost_usd)),
    }));
  }

  /** Per-account spend + quarantine flags (§10.3 "per-account spend with quarantine flags"). */
  async accountSpend(days = 30): Promise<AccountSpendRow[]> {
    if (!(await this.hasEnvSchema())) return [];
    const res = await sql<{
      aws_account_id: string;
      state: string;
      region: string;
      attempt_id: string | null;
      budget_usd: string | null;
      spend_usd: string;
      quarantine_reason: string | null;
    }>`
      SELECT ca.aws_account_id,
             ca.state,
             ca.region,
             ca.attempt_id::text AS attempt_id,
             ca.budget_usd,
             COALESCE((
               SELECT SUM(um.cloud_cost_usd)
                 FROM env.usage_meter um
                WHERE um.attempt_id = ca.attempt_id::text
                  AND um.window_start >= now() - make_interval(days => ${Math.max(1, Math.floor(days))})
             ), 0) AS spend_usd,
             ca.quarantine_reason
        FROM env.cloud_account ca
       ORDER BY spend_usd DESC
    `.execute(this.db);
    return res.rows.map((r) => ({
      aws_account_id: r.aws_account_id,
      state: r.state,
      region: r.region,
      attempt_id: r.attempt_id,
      budget_usd: r.budget_usd === null ? null : Number(r.budget_usd),
      spend_usd: round4(Number(r.spend_usd)),
      quarantined: r.state === 'QUARANTINED',
      quarantine_reason: r.quarantine_reason,
    }));
  }

  /**
   * Budget-breach history — from the ACCOUNT_QUARANTINED / ACCOUNT_NUKED
   * events on the attempt stream (the orchestrator emits these; the
   * analytics ingester carries them into ClickHouse, but the raw source
   * for a small admin view is fine straight from Postgres).
   */
  async budgetBreachHistory(
    tenantId: string,
    limit = 50,
  ): Promise<BudgetBreachRow[]> {
    const res = await sql<{
      aws_account_id: string;
      attempt_id: string | null;
      reason: string;
      resources_remaining: number | null;
      at: string;
    }>`
      SELECT e.payload->>'cloud_account_id'                          AS aws_account_id,
             e.attempt_id::text                                      AS attempt_id,
             COALESCE(e.payload->>'reason', e.type)                  AS reason,
             NULLIF(e.payload->>'resources_remaining','')::int       AS resources_remaining,
             e.occurred_at::text                                     AS at
        FROM attempt.attempt_events e
        JOIN attempt.attempt a ON a.id = e.attempt_id
       WHERE a.tenant_id = ${tenantId}::uuid
         AND e.type IN ('ACCOUNT_QUARANTINED', 'ACCOUNT_NUKED')
       ORDER BY e.occurred_at DESC
       LIMIT ${Math.max(1, Math.min(limit, 500))}
    `.execute(this.db);
    return res.rows.map((r) => ({
      aws_account_id: r.aws_account_id,
      attempt_id: r.attempt_id,
      reason: r.reason,
      resources_remaining: r.resources_remaining,
      at: r.at,
    }));
  }
}

function round4(n: number): number {
  return Math.round((Number.isFinite(n) ? n : 0) * 1e4) / 1e4;
}
