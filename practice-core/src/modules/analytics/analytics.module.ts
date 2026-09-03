import { Module } from '@nestjs/common';
import { DatabaseModule } from '../../db/database.module';
import { ClickHouseClient } from './clickhouse.client';
import { AnalyticsIngestionService } from './analytics-ingestion.service';
import { AnalyticsQueryService } from './analytics-query.service';

/**
 * Phase 3 (PLAN_PHASE3_PROJECTS.md 4.1 / 4.2 / B8). The ClickHouse
 * analytics pipeline: a poll-based ingester tailing `attempt_events` into
 * ClickHouse (4.1) and a query layer that serves rollups from ClickHouse
 * when CLICKHOUSE_URL is set, Postgres otherwise (4.2 — a runtime
 * toggle, so a deployment without ClickHouse still works).
 *
 * Exported for AdminModule (the cost dashboard + analytics endpoints)
 * and for a future `analytics/*` controller.
 */
@Module({
  imports: [DatabaseModule],
  providers: [
    ClickHouseClient,
    AnalyticsIngestionService,
    AnalyticsQueryService,
  ],
  exports: [ClickHouseClient, AnalyticsIngestionService, AnalyticsQueryService],
})
export class AnalyticsModule {}
