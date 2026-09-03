#!/usr/bin/env bash
# Phase 3 2.5 — daily CUR-in-S3 reconciliation. Reads the CUR parquet/csv
# export for a day from the bucket and emits the same JSON shape as ce.sh,
# but with the authoritative (final) cost figures.
#
# The CUR is large; production uses Athena over the CUR table. This
# wrapper runs a parameterised Athena query and returns its result set.
set -euo pipefail
BUCKET="" ; DAY="" ; JSON=0
while [ $# -gt 0 ]; do
  case "$1" in
    --bucket) BUCKET="$2"; shift 2;;
    --day) DAY="$2"; shift 2;;
    --json) JSON=1; shift;;
    *) shift;;
  esac
done
: "${BUCKET:?}" ; : "${DAY:?}" ; : "${PRACTICE_CUR_ATHENA_DB:?set PRACTICE_CUR_ATHENA_DB}" ; : "${PRACTICE_CUR_ATHENA_TABLE:?set PRACTICE_CUR_ATHENA_TABLE}"

QID="$(aws athena start-query-execution \
  --query-string "SELECT line_item_usage_account_id AS account_id,
                         COALESCE(resource_tags_user_attempt_id,'') AS attempt_id,
                         from_iso8601_timestamp('${DAY}T00:00:00Z') AS window_start,
                         from_iso8601_timestamp('${DAY}T00:00:00Z') + interval '1' day AS window_end,
                         SUM(line_item_unblended_cost) AS amount_usd
                  FROM ${PRACTICE_CUR_ATHENA_DB}.${PRACTICE_CUR_ATHENA_TABLE}
                  WHERE line_item_usage_start_date >= from_iso8601_timestamp('${DAY}T00:00:00Z')
                    AND line_item_usage_start_date <  from_iso8601_timestamp('${DAY}T00:00:00Z') + interval '1' day
                  GROUP BY 1,2" \
  --result-configuration "OutputLocation=s3://${BUCKET}/athena-results/" \
  --query 'QueryExecutionId' --output text)"

# poll
for _ in $(seq 1 60); do
  ST="$(aws athena get-query-execution --query-execution-id "$QID" --query 'QueryExecution.Status.State' --output text)"
  [ "$ST" = "SUCCEEDED" ] && break
  case "$ST" in FAILED|CANCELLED) echo "athena query $ST" >&2; exit 1;; esac
  sleep 2
done

aws athena get-query-results --query-execution-id "$QID" --output json \
| jq -c '
    ( .ResultSet.Rows[1:] ) as $rows
    | [ $rows[] | .Data
        | { account_id: .[0].VarCharValue,
            attempt_id: .[1].VarCharValue,
            window_start: .[2].VarCharValue,
            window_end: .[3].VarCharValue,
            amount_usd: ( .[4].VarCharValue | tonumber ) } ]'
