#!/usr/bin/env bash
# Phase 3 2.3 — create/update a per-account AWS Budget with alarm
# notifications at the given thresholds, wired EventBridge -> SNS ->
# the orchestrator's /cloud/budget-breach endpoint.
#
#   infra/budget/put.sh --account <id> --limit 25.00 --thresholds 50,80,100
#
# Env: PRACTICE_BUDGET_SNS_TOPIC_ARN must be set (the SNS topic the
# orchestrator subscribes to; created by infra/aws-org or a small
# companion module).
set -euo pipefail
ACCOUNT="" ; LIMIT="" ; THRESHOLDS="50,80,100"
while [ $# -gt 0 ]; do
  case "$1" in
    --account) ACCOUNT="$2"; shift 2;;
    --limit) LIMIT="$2"; shift 2;;
    --thresholds) THRESHOLDS="$2"; shift 2;;
    *) echo "unknown arg: $1" >&2; exit 2;;
  esac
done
: "${ACCOUNT:?}" ; : "${LIMIT:?}" ; : "${PRACTICE_BUDGET_SNS_TOPIC_ARN:?set PRACTICE_BUDGET_SNS_TOPIC_ARN}"

BUDGET_NAME="practice-sandbox-${ACCOUNT}"
NOTIFS="$(mktemp)"; trap 'rm -f "$NOTIFS"' EXIT
{
  echo "["
  IFS=',' read -ra PCTS <<< "$THRESHOLDS"
  for i in "${!PCTS[@]}"; do
    [ "$i" -gt 0 ] && echo ","
    cat <<JSON
  {
    "Notification": {"NotificationType":"ACTUAL","ComparisonOperator":"GREATER_THAN","Threshold":${PCTS[$i]},"ThresholdType":"PERCENTAGE"},
    "Subscribers": [{"SubscriptionType":"SNS","Address":"${PRACTICE_BUDGET_SNS_TOPIC_ARN}"}]
  }
JSON
  done
  echo "]"
} > "$NOTIFS"

aws budgets create-budget \
  --account-id "$ACCOUNT" \
  --budget "{\"BudgetName\":\"${BUDGET_NAME}\",\"BudgetLimit\":{\"Amount\":\"${LIMIT}\",\"Unit\":\"USD\"},\"TimeUnit\":\"MONTHLY\",\"BudgetType\":\"COST\"}" \
  --notifications-with-subscribers "file://${NOTIFS}" 2>/dev/null \
|| aws budgets update-budget \
  --account-id "$ACCOUNT" \
  --new-budget "{\"BudgetName\":\"${BUDGET_NAME}\",\"BudgetLimit\":{\"Amount\":\"${LIMIT}\",\"Unit\":\"USD\"},\"TimeUnit\":\"MONTHLY\",\"BudgetType\":\"COST\"}"
echo "budget ${BUDGET_NAME} set to \$${LIMIT} with thresholds ${THRESHOLDS}"
