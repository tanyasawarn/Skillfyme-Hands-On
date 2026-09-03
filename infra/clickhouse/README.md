# infra/clickhouse — Phase 3 analytics store

Phase 3 (PLAN_PHASE3_PROJECTS.md **1.5**, decision **D-P3-6**). ClickHouse
is where the `attempt_events` analytics stream is ingested and rolled up
(Stage 4.1 / B8), migrating `admin/analytics.service.ts` off the Postgres
read-replica.

**D-P3-6 recommendation: ClickHouse Cloud dev tier for Phase 3** (lower
ops load while the pipeline is new); revisit self-hosted in Phase 5 if
volume/cost warrants.

| path | what | status |
|---|---|---|
| `compose/docker-compose.clickhouse.yml` | a real single-node ClickHouse on the local compose stack (profile `clickhouse`). Used to build + test the ingestion consumer (B8) today. | **works now** |
| `compose/init/01-schema.sql` | the `attempt_events` table + the hourly rollup materialised views, mounted into the container's initdb. | works now |
| `cloud/` | ClickHouse Cloud service provisioning (`clickhouse_cloud` Terraform provider) + the same schema applied via the ClickHouse client. | authored; `tofu validate` clean; **apply `[B]`** (needs a ClickHouse Cloud API key) |
| `scripts/verify-clickhouse.sh` | round-trip: insert a synthetic `attempt_events` row, confirm it lands, confirm the hourly rollup view aggregates it. | runs against the compose deploy |

## Run the local deploy

```
docker compose --profile clickhouse -f docker-compose.yml \
  -f infra/clickhouse/compose/docker-compose.clickhouse.yml up -d

infra/clickhouse/scripts/verify-clickhouse.sh
```

ClickHouse HTTP is on <http://localhost:8123> (user `default`, no
password locally). `practice-core` reads `CLICKHOUSE_URL` — the B8
ingestion consumer connects there.

## Cloud apply (blocked)

`cloud/` needs a ClickHouse Cloud organisation + API key
(`CLICKHOUSE_CLOUD_TOKEN_KEY` / `..._TOKEN_SECRET`). Until then this item
is `[B]` on apply; the schema + local deploy that the ingestion code is
built against are complete.
