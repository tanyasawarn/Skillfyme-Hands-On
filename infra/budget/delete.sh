#!/usr/bin/env bash
set -euo pipefail
ACCOUNT=""
while [ $# -gt 0 ]; do case "$1" in --account) ACCOUNT="$2"; shift 2;; *) shift;; esac; done
: "${ACCOUNT:?}"
aws budgets delete-budget --account-id "$ACCOUNT" --budget-name "practice-sandbox-${ACCOUNT}" 2>/dev/null || true
echo "budget for ${ACCOUNT} removed"
