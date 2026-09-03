#!/usr/bin/env bash
# Phase 3 1.5 — load the analytics schema into a ClickHouse Cloud service.
# Run after `tofu apply` in this dir. Uses the same 01-schema.sql the
# local compose deploy uses, so the two stay identical.
#
#   CH_HOST=<service_endpoint_https from tofu output> \
#   CH_USER=default CH_PASSWORD=<the password behind service_password_hash> \
#     infra/clickhouse/cloud/apply-schema.sh
set -euo pipefail

CH_HOST="${CH_HOST:?set CH_HOST (tofu output service_endpoint_https)}"
CH_USER="${CH_USER:-default}"
CH_PASSWORD="${CH_PASSWORD:?set CH_PASSWORD}"
SCHEMA="$(dirname "$0")/../compose/init/01-schema.sql"

echo "applying $SCHEMA to https://${CH_HOST}:8443 ..."
curl -sf "https://${CH_HOST}:8443/" \
  --user "${CH_USER}:${CH_PASSWORD}" \
  --data-binary "@${SCHEMA}"
echo "done. verify with: CLICKHOUSE_URL=https://${CH_HOST}:8443 CLICKHOUSE_PASSWORD=... infra/clickhouse/scripts/verify-clickhouse.sh"
