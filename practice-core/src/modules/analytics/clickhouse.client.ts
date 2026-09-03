import { Injectable, Logger } from '@nestjs/common';
import { ConfigService } from '@nestjs/config';

/**
 * Phase 3 (PLAN_PHASE3_PROJECTS.md 4.1 / B8). A tiny HTTP client for
 * ClickHouse — the analytics store (`infra/clickhouse`, D-P3-6).
 *
 * ClickHouse ships no first-party Node driver in this repo's deps; its
 * HTTP interface is plenty for the two things we do — INSERT the
 * `attempt_events` stream and run aggregate SELECTs for the admin
 * dashboard. Uses global fetch (Node ≥ 18).
 *
 * Enabled iff CLICKHOUSE_URL is set; `isEnabled()` lets the ingestion
 * consumer and the analytics query layer no-op / fall back to Postgres
 * otherwise (4.2).
 */
@Injectable()
export class ClickHouseClient {
  private readonly logger = new Logger(ClickHouseClient.name);
  private readonly url: string;
  private readonly database: string;
  private readonly user: string;
  private readonly password: string;
  private readonly timeoutMs: number;

  constructor(config: ConfigService) {
    this.url = (config.get<string>('CLICKHOUSE_URL') ?? '').replace(/\/+$/, '');
    this.database =
      config.get<string>('CLICKHOUSE_DATABASE') ?? 'practice_analytics';
    this.user = config.get<string>('CLICKHOUSE_USER') ?? 'default';
    this.password = config.get<string>('CLICKHOUSE_PASSWORD') ?? '';
    this.timeoutMs = Number(
      config.get<string>('CLICKHOUSE_TIMEOUT_MS') ?? '15000',
    );
  }

  isEnabled(): boolean {
    return this.url.length > 0;
  }

  private async exec(
    body: string,
    params?: Record<string, string>,
  ): Promise<string> {
    const qs = new URLSearchParams({
      database: this.database,
      ...(params ?? {}),
    });
    const controller = new AbortController();
    const timer = setTimeout(() => controller.abort(), this.timeoutMs);
    try {
      const res = await fetch(`${this.url}/?${qs.toString()}`, {
        method: 'POST',
        headers: {
          authorization:
            'Basic ' +
            Buffer.from(`${this.user}:${this.password}`).toString('base64'),
        },
        body,
        signal: controller.signal,
      });
      const text = await res.text();
      if (!res.ok) {
        throw new Error(
          `ClickHouse ${res.status}: ${text.slice(0, 400)} (query: ${body.slice(0, 200)})`,
        );
      }
      return text;
    } finally {
      clearTimeout(timer);
    }
  }

  /** Run a SELECT, returning rows as objects (FORMAT JSONEachRow appended). */
  async query<T = Record<string, unknown>>(sql: string): Promise<T[]> {
    const withFormat = /format\s+\w+\s*;?\s*$/i.test(sql)
      ? sql
      : `${sql.replace(/;\s*$/, '')} FORMAT JSONEachRow`;
    const text = await this.exec(withFormat);
    return text
      .split('\n')
      .filter((l) => l.trim().length > 0)
      .map((l) => JSON.parse(l) as T);
  }

  /** Run a statement that returns nothing (DDL, INSERT ... VALUES). */
  async command(sql: string): Promise<void> {
    await this.exec(sql);
  }

  /**
   * Bulk-insert rows into a table using JSONEachRow. `rows` are plain
   * objects whose keys match the table columns.
   */
  async insert(
    table: string,
    rows: Array<Record<string, unknown>>,
  ): Promise<void> {
    if (rows.length === 0) return;
    const body =
      `INSERT INTO ${table} FORMAT JSONEachRow\n` +
      rows.map((r) => JSON.stringify(r)).join('\n');
    await this.exec(body);
  }

  /** Cheap health probe used by tests + a readiness check. */
  async ping(): Promise<boolean> {
    try {
      const res = await fetch(`${this.url}/ping`);
      return res.ok;
    } catch {
      return false;
    }
  }
}
