import { ConfigService } from '@nestjs/config';
import type { Kysely } from 'kysely';
import { sql } from 'kysely';
import type { Database } from '../../src/db/schema';
import { ClickHouseClient } from '../../src/modules/analytics/clickhouse.client';
import { AnalyticsIngestionService } from '../../src/modules/analytics/analytics-ingestion.service';
import { AnalyticsQueryService } from '../../src/modules/analytics/analytics-query.service';
import { createTestDb, truncateAll } from './test-db';

/**
 * Phase 3 (PLAN_PHASE3_PROJECTS.md 4.1 / 4.2 / B8). The ClickHouse
 * analytics pipeline end-to-end: seed attempt_events in Postgres →
 * AnalyticsIngestionService.tick() ships them to ClickHouse → the
 * materialised-view rollup aggregates → AnalyticsQueryService returns
 * the same numbers from ClickHouse and Postgres (the 4.2 "identical
 * numbers" check).
 *
 * Skips unless CLICKHOUSE_URL is set (run the compose clickhouse profile
 * first). The Postgres-only half of the query service is always tested.
 */
const CH_URL = process.env.CLICKHOUSE_URL;
const withCh = CH_URL ? describe : describe.skip;

function config(overrides: Record<string, string> = {}): ConfigService {
  const map: Record<string, string> = {
    CLICKHOUSE_URL: CH_URL ?? '',
    CLICKHOUSE_DATABASE: 'practice_analytics',
    ANALYTICS_INGESTION: 'off', // don't auto-start the loop; tests drive tick()
    ANALYTICS_INGESTION_BATCH: '500',
    ...overrides,
  };
  return { get: (k: string) => map[k] } as unknown as ConfigService;
}

describe('Analytics pipeline (integration) — Phase 3 4.1/4.2', () => {
  let db: Kysely<Database>;
  let tenantId: string;
  let activityId: string;
  let attemptId: string;

  beforeAll(() => {
    db = createTestDb();
  });
  afterAll(async () => {
    await db.destroy();
  });

  beforeEach(async () => {
    await truncateAll(db);
    const tenant = await db
      .insertInto('learner.tenant')
      .values({ name: 't' })
      .returningAll()
      .executeTakeFirstOrThrow();
    tenantId = tenant.id;
    const user = await db
      .insertInto('learner.user_account')
      .values({ tenant_id: tenantId, email: 'a@t.dev' })
      .returningAll()
      .executeTakeFirstOrThrow();
    const activity = await db
      .insertInto('content.activity')
      .values({ tenant_id: tenantId, slug: 'proj.x', mode: 'PROJECT' })
      .returningAll()
      .executeTakeFirstOrThrow();
    activityId = activity.id;
    const version = await db
      .insertInto('content.activity_version')
      .values({
        activity_id: activityId,
        version: 1,
        status: 'PUBLISHED',
        spec_jsonb: {},
      })
      .returningAll()
      .executeTakeFirstOrThrow();
    const attempt = await db
      .insertInto('attempt.attempt')
      .values({
        tenant_id: tenantId,
        user_id: user.id,
        activity_id: activityId,
        activity_version_id: version.id,
        mode: 'PROJECT',
      })
      .returningAll()
      .executeTakeFirstOrThrow();
    attemptId = attempt.id;
  });

  async function seedEvents(n: number, milestoneGated: number): Promise<void> {
    for (let i = 0; i < n; i++) {
      const type =
        i < milestoneGated
          ? 'MILESTONE_GATED'
          : i < milestoneGated * 2
            ? 'DEFENCE_MESSAGE'
            : 'MILESTONE_SUBMITTED';
      await db
        .insertInto('attempt.attempt_events')
        .values({
          attempt_id: attemptId,
          seq: String(i + 1),
          occurred_at: new Date(),
          actor: 'SYSTEM',
          type,
          payload: { i },
        })
        .execute();
    }
  }

  it('Postgres fallback: query service aggregates the source table when ClickHouse is off', async () => {
    await seedEvents(10, 3);
    const q = new AnalyticsQueryService(
      db,
      new ClickHouseClient(config({ CLICKHOUSE_URL: '' })),
    );
    expect(q.store()).toBe('postgres');
    const rows = await q.eventRollup(tenantId, 'activity', 24);
    expect(rows).toHaveLength(1);
    expect(rows[0].bucket).toBe(activityId);
    expect(rows[0].events).toBe(10);
    expect(rows[0].milestones_gated).toBe(3);
    expect(rows[0].defence_msgs).toBe(3);
    expect(rows[0].distinct_attempts).toBe(1);
  });

  withCh('ClickHouse: ingest → rollup → identical numbers vs Postgres', () => {
    let ch: ClickHouseClient;

    beforeAll(async () => {
      ch = new ClickHouseClient(config());
      // clear the raw table between runs
      await ch.command(
        `ALTER TABLE practice_analytics.attempt_events DELETE WHERE 1=1`,
      );
    });

    it('tick() ships events to ClickHouse and both stores return the same rollup', async () => {
      await seedEvents(20, 5);

      const ingest = new AnalyticsIngestionService(db, ch, config());
      // reset the cursor so this run re-ingests
      await sql`DELETE FROM analytics.ingestion_cursor WHERE id = 'attempt_events'`
        .execute(db)
        .catch(() => undefined);
      const n = await ingest.tick();
      expect(n).toBe(20);

      // wait for the MV to settle
      await new Promise((r) => setTimeout(r, 500));

      const chQuery = new AnalyticsQueryService(db, ch);
      const pgQuery = new AnalyticsQueryService(
        db,
        new ClickHouseClient(config({ CLICKHOUSE_URL: '' })),
      );
      expect(chQuery.store()).toBe('clickhouse');

      const chRows = await chQuery.eventRollup(tenantId, 'activity', 24);
      const pgRows = await pgQuery.eventRollup(tenantId, 'activity', 24);

      const norm = (rows: Awaited<ReturnType<typeof chQuery.eventRollup>>) =>
        rows
          .map((r) => ({
            bucket: r.bucket,
            events: r.events,
            milestones_gated: r.milestones_gated,
            defence_msgs: r.defence_msgs,
            distinct_attempts: r.distinct_attempts,
          }))
          .sort((a, b) => a.bucket.localeCompare(b.bucket));

      expect(norm(chRows)).toEqual(norm(pgRows));
      expect(chRows[0].events).toBe(20);
      expect(chRows[0].milestones_gated).toBe(5);
    }, 30_000);

    it('tick() is idempotent per cursor — re-running does not double-ingest', async () => {
      const ingest = new AnalyticsIngestionService(db, ch, config());
      // make sure the cursor table exists, then clear it so this run
      // starts from the beginning of the (freshly truncated) events table
      await ingest.tick(); // creates analytics.ingestion_cursor
      await sql`DELETE FROM analytics.ingestion_cursor WHERE id = 'attempt_events'`.execute(
        db,
      );

      await seedEvents(5, 1);
      const first = await ingest.tick();
      const second = await ingest.tick();
      expect(first).toBe(5);
      expect(second).toBe(0);
    }, 30_000);
  });
});
