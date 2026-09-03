import {
  Inject,
  Injectable,
  Logger,
  OnModuleDestroy,
  OnModuleInit,
} from '@nestjs/common';
import { ConfigService } from '@nestjs/config';
import { sql, type Kysely } from 'kysely';
import { KYSELY } from '../../db/database.module';
import type { Database } from '../../db/schema';
import { ClickHouseClient } from './clickhouse.client';

/**
 * Phase 3 (PLAN_PHASE3_PROJECTS.md 4.1 / B8). Ingests the canonical
 * `attempt_events` stream into ClickHouse.
 *
 * The plan describes a NATS/Kafka fan-out consumer. This codebase writes
 * `attempt_events` to Postgres only (there is no NATS subject carrying
 * the whole stream yet), so the ingester **tails
 * `attempt.attempt_events` by `id`** with a durable cursor
 * (`analytics.ingestion_cursor`), enriches each row with tenant / course
 * / activity / user from `attempt`, and bulk-inserts into
 * `practice_analytics.attempt_events`. Same "one source, many readers"
 * shape as a fan-out; swapping the source to a NATS consumer later is a
 * localised change (replace `fetchBatch`).
 *
 * The hourly rollups (`attempt_hourly` and up) are maintained by the
 * ClickHouse materialised view (`infra/clickhouse/compose/init/01-schema.sql`),
 * so this service only feeds the raw table.
 *
 * No-ops entirely when CLICKHOUSE_URL is unset.
 */
@Injectable()
export class AnalyticsIngestionService
  implements OnModuleInit, OnModuleDestroy
{
  private readonly logger = new Logger(AnalyticsIngestionService.name);
  private readonly enabled: boolean;
  private readonly intervalMs: number;
  private readonly batchSize: number;
  private timer: NodeJS.Timeout | null = null;
  private running = false;

  constructor(
    @Inject(KYSELY) private readonly db: Kysely<Database>,
    private readonly ch: ClickHouseClient,
    config: ConfigService,
  ) {
    this.enabled =
      this.ch.isEnabled() &&
      config.get<string>('ANALYTICS_INGESTION') !== 'off';
    this.intervalMs = Number(
      config.get<string>('ANALYTICS_INGESTION_INTERVAL_MS') ?? '5000',
    );
    this.batchSize = Number(
      config.get<string>('ANALYTICS_INGESTION_BATCH') ?? '1000',
    );
  }

  onModuleInit(): void {
    if (!this.enabled) {
      this.logger.log(
        'analytics ingestion disabled (CLICKHOUSE_URL unset or ANALYTICS_INGESTION=off)',
      );
      return;
    }
    void this.ensureCursorTable();
    this.timer = setInterval(() => {
      void this.tick();
    }, this.intervalMs);
    this.logger.log(
      `analytics ingestion enabled: polling attempt_events every ${this.intervalMs}ms`,
    );
  }

  onModuleDestroy(): void {
    if (this.timer) clearInterval(this.timer);
  }

  /** One ingestion pass. Public so tests + an admin endpoint can drive it. */
  async tick(): Promise<number> {
    if (this.running) return 0;
    this.running = true;
    try {
      await this.ensureCursorTable();
      const cursor = await this.getCursor();
      const rows = await this.fetchBatch(cursor);
      if (rows.length === 0) return 0;
      await this.ch.insert(
        'practice_analytics.attempt_events',
        rows.map((r) => ({
          attempt_id: r.attempt_id,
          tenant_id: r.tenant_id,
          activity_id: r.activity_id,
          course_id: r.course_id ?? '',
          user_id: r.user_id,
          seq: Number(r.seq),
          type: r.type,
          actor: r.actor,
          occurred_at: toClickHouseDateTime(r.occurred_at),
          payload: JSON.stringify(r.payload ?? {}),
        })),
      );
      const maxId = rows[rows.length - 1].id;
      await this.setCursor(maxId);
      this.logger.debug(
        `ingested ${rows.length} attempt_events into ClickHouse (cursor → ${maxId})`,
      );
      return rows.length;
    } catch (e) {
      this.logger.error(
        `analytics ingestion tick failed: ${e instanceof Error ? e.message : String(e)}`,
      );
      return 0;
    } finally {
      this.running = false;
    }
  }

  /** Total rows in the ClickHouse raw table — used by verify + tests. */
  async ingestedCount(): Promise<number> {
    const rows = await this.ch.query<{ c: string }>(
      'SELECT count() AS c FROM practice_analytics.attempt_events',
    );
    return Number(rows[0]?.c ?? 0);
  }

  // --- internals -----------------------------------------------------

  private cursorTableReady = false;

  private async ensureCursorTable(): Promise<void> {
    if (this.cursorTableReady) return;
    // attempt_events.id is a bigserial (bigint), PK is
    // (attempt_id, seq, occurred_at) — so the cursor is the max id seen,
    // stored as a bigint. Kept as text on the JS side (pg returns bigint
    // as string).
    await sql`
      CREATE SCHEMA IF NOT EXISTS analytics;
      CREATE TABLE IF NOT EXISTS analytics.ingestion_cursor (
        id            text PRIMARY KEY DEFAULT 'attempt_events',
        last_event_id bigint,
        updated_at    timestamptz NOT NULL DEFAULT now()
      );
    `.execute(this.db);
    // One-time migration for a dev DB that ran an earlier build where
    // last_event_id was uuid. Only fires if the column type is wrong.
    await sql`
      DO $$
      BEGIN
        IF EXISTS (
          SELECT 1 FROM information_schema.columns
           WHERE table_schema = 'analytics' AND table_name = 'ingestion_cursor'
             AND column_name = 'last_event_id' AND data_type = 'uuid'
        ) THEN
          ALTER TABLE analytics.ingestion_cursor
            ALTER COLUMN last_event_id TYPE bigint USING NULL;
        END IF;
      END $$;
    `.execute(this.db);
    this.cursorTableReady = true;
  }

  private async getCursor(): Promise<string | null> {
    const res = await sql<{ last_event_id: string | null }>`
      SELECT last_event_id::text AS last_event_id
        FROM analytics.ingestion_cursor WHERE id = 'attempt_events'
    `.execute(this.db);
    return res.rows[0]?.last_event_id ?? null;
  }

  private async setCursor(eventId: string): Promise<void> {
    await sql`
      INSERT INTO analytics.ingestion_cursor (id, last_event_id, updated_at)
      VALUES ('attempt_events', ${eventId}::bigint, now())
      ON CONFLICT (id) DO UPDATE SET last_event_id = EXCLUDED.last_event_id, updated_at = now()
    `.execute(this.db);
  }

  private async fetchBatch(cursor: string | null): Promise<
    Array<{
      id: string;
      attempt_id: string;
      seq: string;
      occurred_at: Date;
      actor: string;
      type: string;
      payload: unknown;
      tenant_id: string;
      activity_id: string;
      course_id: string | null;
      user_id: string;
    }>
  > {
    // Order by id so the cursor is a total order; enrich from attempt.
    // course_id: activities map to a course via activity_topic → topic →
    // module → course. That link is optional (not every activity is in a
    // course), so it's a LEFT JOIN chain and comes through as '' when
    // absent — the dashboard's primary grain is tenant/activity/attempt.
    const rows = await sql<{
      id: string;
      attempt_id: string;
      seq: string;
      occurred_at: Date;
      actor: string;
      type: string;
      payload: unknown;
      tenant_id: string;
      activity_id: string;
      course_id: string | null;
      user_id: string;
    }>`
      SELECT e.id::text AS id, e.attempt_id, e.seq::text AS seq, e.occurred_at,
             e.actor, e.type, e.payload,
             a.tenant_id, a.activity_id, a.user_id,
             mod.course_id
        FROM attempt.attempt_events e
        JOIN attempt.attempt a ON a.id = e.attempt_id
        LEFT JOIN LATERAL (
          SELECT m.course_id
            FROM content.activity_topic at
            JOIN content.topic t   ON t.id = at.topic_id
            JOIN content.module m  ON m.id = t.module_id
           WHERE at.activity_version_id = a.activity_version_id
           LIMIT 1
        ) mod ON true
       WHERE (${cursor}::bigint IS NULL OR e.id > ${cursor}::bigint)
       ORDER BY e.id ASC
       LIMIT ${this.batchSize}
    `.execute(this.db);
    return rows.rows;
  }
}

function toClickHouseDateTime(d: Date | string): string {
  const dt = typeof d === 'string' ? new Date(d) : d;
  // ClickHouse DateTime64(3) accepts 'YYYY-MM-DD HH:MM:SS.mmm'
  return dt.toISOString().replace('T', ' ').replace('Z', '');
}
