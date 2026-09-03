import { Inject, Injectable } from '@nestjs/common';
import { sql, type Kysely } from 'kysely';
import { KYSELY } from '../../db/database.module';
import type { Database } from '../../db/schema';
import { ClickHouseClient } from './clickhouse.client';

/**
 * Phase 3 (PLAN_PHASE3_PROJECTS.md 4.1 / 4.2 / B8). The read side of the
 * analytics pipeline: hourly rollups attempt → activity → course →
 * tenant → global, served from ClickHouse when CLICKHOUSE_URL is set,
 * and from Postgres otherwise (the 4.2 migration is a runtime toggle, so
 * a deployment without ClickHouse still works — Postgres stays the OLTP
 * source of truth either way).
 *
 * `store()` reports which backend answered — the 4.2 acceptance check
 * ("verify identical numbers") runs the same query against both and
 * diffs.
 */
export type RollupGrain = 'activity' | 'course' | 'tenant' | 'global';

export interface EventRollupRow {
  bucket: string; // the grain key ('' for global)
  events: number;
  milestones_gated: number;
  defence_msgs: number;
  distinct_attempts: number;
}

@Injectable()
export class AnalyticsQueryService {
  constructor(
    @Inject(KYSELY) private readonly db: Kysely<Database>,
    private readonly ch: ClickHouseClient,
  ) {}

  store(): 'clickhouse' | 'postgres' {
    return this.ch.isEnabled() ? 'clickhouse' : 'postgres';
  }

  /**
   * Event counts + milestone/defence counts + distinct attempts for a
   * tenant, grouped at `grain`, over the last `hours` hours.
   */
  async eventRollup(
    tenantId: string,
    grain: RollupGrain,
    hours = 24,
  ): Promise<EventRollupRow[]> {
    return this.ch.isEnabled()
      ? this.eventRollupClickHouse(tenantId, grain, hours)
      : this.eventRollupPostgres(tenantId, grain, hours);
  }

  private groupCol(grain: RollupGrain): string {
    switch (grain) {
      case 'activity':
        return 'activity_id';
      case 'course':
        return 'course_id';
      case 'tenant':
        return 'tenant_id';
      case 'global':
        return "''";
    }
  }

  private async eventRollupClickHouse(
    tenantId: string,
    grain: RollupGrain,
    hours: number,
  ): Promise<EventRollupRow[]> {
    const col = this.groupCol(grain);
    const rows = await this.ch.query<{
      bucket: string;
      events: string;
      milestones_gated: string;
      defence_msgs: string;
      distinct_attempts: string;
    }>(`
      SELECT ${col} AS bucket,
             count()                              AS events,
             countIf(type = 'MILESTONE_GATED')    AS milestones_gated,
             countIf(type = 'DEFENCE_MESSAGE')    AS defence_msgs,
             uniqExact(attempt_id)                AS distinct_attempts
        FROM practice_analytics.attempt_events
       WHERE tenant_id = '${escapeCh(tenantId)}'
         AND occurred_at >= now() - INTERVAL ${Math.max(1, Math.floor(hours))} HOUR
       GROUP BY bucket
       ORDER BY events DESC
    `);
    return rows.map((r) => ({
      bucket: r.bucket,
      events: Number(r.events),
      milestones_gated: Number(r.milestones_gated),
      defence_msgs: Number(r.defence_msgs),
      distinct_attempts: Number(r.distinct_attempts),
    }));
  }

  private async eventRollupPostgres(
    tenantId: string,
    grain: RollupGrain,
    hours: number,
  ): Promise<EventRollupRow[]> {
    // Postgres fallback: aggregate the source table with the same
    // enrichment join the ingester uses.
    const bucketExpr =
      grain === 'activity'
        ? sql`a.activity_id::text`
        : grain === 'course'
          ? sql`COALESCE(mod.course_id::text, '')`
          : grain === 'tenant'
            ? sql`a.tenant_id::text`
            : sql`''`;

    const res = await sql<{
      bucket: string;
      events: string;
      milestones_gated: string;
      defence_msgs: string;
      distinct_attempts: string;
    }>`
      SELECT ${bucketExpr} AS bucket,
             count(*)                                                     AS events,
             count(*) FILTER (WHERE e.type = 'MILESTONE_GATED')           AS milestones_gated,
             count(*) FILTER (WHERE e.type = 'DEFENCE_MESSAGE')           AS defence_msgs,
             count(DISTINCT e.attempt_id)                                 AS distinct_attempts
        FROM attempt.attempt_events e
        JOIN attempt.attempt a ON a.id = e.attempt_id
        LEFT JOIN LATERAL (
          SELECT m.course_id
            FROM content.activity_topic at
            JOIN content.topic t  ON t.id = at.topic_id
            JOIN content.module m ON m.id = t.module_id
           WHERE at.activity_version_id = a.activity_version_id
           LIMIT 1
        ) mod ON true
       WHERE a.tenant_id = ${tenantId}::uuid
         AND e.occurred_at >= now() - make_interval(hours => ${Math.max(1, Math.floor(hours))})
       GROUP BY 1
       ORDER BY events DESC
    `.execute(this.db);

    return res.rows.map((r) => ({
      bucket: r.bucket,
      events: Number(r.events),
      milestones_gated: Number(r.milestones_gated),
      defence_msgs: Number(r.defence_msgs),
      distinct_attempts: Number(r.distinct_attempts),
    }));
  }
}

function escapeCh(s: string): string {
  return s.replace(/'/g, "''").replace(/\\/g, '\\\\');
}
