#!/usr/bin/env bash
# Phase 3 2.5 — Cost Explorer read for one account since a date, grouped
# by the attempt_id tag. Emits a JSON array of
# {account_id, attempt_id, window_start, window_end, amount_usd}.
set -euo pipefail
ACCOUNT="" ; SINCE="" ; JSON=0
while [ $# -gt 0 ]; do
  case "$1" in
    --account) ACCOUNT="$2"; shift 2;;
    --since) SINCE="$2"; shift 2;;
    --json) JSON=1; shift;;
    *) shift;;
  esac
done
: "${ACCOUNT:?}" ; : "${SINCE:?}"
END="$(date -u +%Y-%m-%d)"

aws ce get-cost-and-usage \
  --time-period "Start=${SINCE},End=${END}" \
  --granularity DAILY \
  --metrics UnblendedCost \
  --filter "{\"Dimensions\":{\"Key\":\"LINKED_ACCOUNT\",\"Values\":[\"${ACCOUNT}\"]}}" \
  --group-by "Type=TAG,Key=attempt_id" \
  --output json \
| jq -c --arg acct "$ACCOUNT" '
    [ .ResultsByTime[] as $t
      | $t.Groups[]
      | { account_id: $acct,
          attempt_id: ( .Keys[0] | sub("^attempt_id\\$"; "") ),
          window_start: ($t.TimePeriod.Start + "T00:00:00Z"),
          window_end:   ($t.TimePeriod.End   + "T00:00:00Z"),
          amount_usd:   ( .Metrics.UnblendedCost.Amount | tonumber ) } ]'
