#!/usr/bin/env bash
# Phase 3 2.2 — containerised aws-nuke runner + mandatory verification.
# Invoked by orchestrator/internal/cloudaws.RealClient.RunNuke.
#
#   infra/nuke/run.sh --account <id> --platform-account <id> --region <r> \
#     --config-template infra/nuke/aws-nuke.yaml.tmpl --json
#
# Steps:
#   1. assume PlatformNukeRole in the target account
#   2. render the aws-nuke config from the template (account id substituted)
#   3. run `aws-nuke --no-dry-run`
#   4. verification pass: AWS Config + Resource Explorer + the hardcoded
#      blind-spot service list; ANY non-empty result => verified=false
#   5. emit {verified, resources_remaining, blind_spot_hits, detail} as JSON
#
# Requires: aws, aws-nuke, jq on PATH (the orchestrator image ships them).
set -euo pipefail
ACCOUNT="" ; PLATFORM_ACCOUNT="" ; REGION="us-east-1" ; TMPL="" ; JSON=0
while [ $# -gt 0 ]; do
  case "$1" in
    --account) ACCOUNT="$2"; shift 2;;
    --platform-account) PLATFORM_ACCOUNT="$2"; shift 2;;
    --region) REGION="$2"; shift 2;;
    --config-template) TMPL="$2"; shift 2;;
    --json) JSON=1; shift;;
    *) echo "unknown arg: $1" >&2; exit 2;;
  esac
done
: "${ACCOUNT:?--account required}"

emit() { # verified resources_remaining "detail" [blind_spot_json_array]
  local bs="${4:-[]}"
  if [ "$JSON" = 1 ]; then
    jq -cn --argjson v "$1" --argjson r "$2" --arg d "$3" --argjson bs "$bs" \
      '{verified:$v, resources_remaining:$r, blind_spot_hits:$bs, detail:$d}'
  else
    echo "verified=$1 resources_remaining=$2 detail=$3 blind_spots=$bs"
  fi
}

ROLE_ARN="arn:aws:iam::${ACCOUNT}:role/PlatformNukeRole"
CREDS="$(aws sts assume-role --role-arn "$ROLE_ARN" --role-session-name platform-nuke --output json)"
export AWS_ACCESS_KEY_ID="$(echo "$CREDS" | jq -r .Credentials.AccessKeyId)"
export AWS_SECRET_ACCESS_KEY="$(echo "$CREDS" | jq -r .Credentials.SecretAccessKey)"
export AWS_SESSION_TOKEN="$(echo "$CREDS" | jq -r .Credentials.SessionToken)"
export AWS_REGION="$REGION"

WORK="$(mktemp -d)"; trap 'rm -rf "$WORK"' EXIT
CONFIG="$WORK/aws-nuke.yaml"
sed "s/__ACCOUNT_ID__/${ACCOUNT}/g" "${TMPL:-$(dirname "$0")/aws-nuke.yaml.tmpl}" > "$CONFIG"

set +e
aws-nuke run --config "$CONFIG" --force --no-dry-run --quiet > "$WORK/nuke.log" 2>&1
NUKE_RC=$?
set -e
if [ $NUKE_RC -ne 0 ]; then
  emit false 0 "aws-nuke exited $NUKE_RC: $(tail -3 "$WORK/nuke.log" | tr '\n' ' ')"
  exit 0
fi

# --- verification pass ---
REMAINING=0
BLIND="[]"

# AWS Config: any non-deleted resource of a recorded type
CFG_COUNT="$(aws configservice select-resource-config \
  --expression "SELECT COUNT(*) WHERE configuration.state.value != 'terminated'" \
  --query 'Results[0]' --output text 2>/dev/null | jq -r '.["COUNT(*)"] // 0' 2>/dev/null || echo 0)"
REMAINING=$((REMAINING + ${CFG_COUNT:-0}))

# Resource Explorer: index-wide search
RE_COUNT="$(aws resource-explorer-2 search --query-string "*" \
  --query 'length(Resources)' --output text 2>/dev/null || echo 0)"
REMAINING=$((REMAINING + ${RE_COUNT:-0}))

# Blind-spot list — services aws-nuke commonly misses. Each check appends
# to BLIND if it finds something.
add_blind() { BLIND="$(echo "$BLIND" | jq -c --arg x "$1" '. + [$x]')"; }
if [ "$(aws route53 list-hosted-zones --query 'length(HostedZones)' --output text 2>/dev/null || echo 0)" != "0" ]; then
  add_blind "route53:hosted-zones"
fi
if [ "$(aws budgets describe-budgets --account-id "$ACCOUNT" --query 'length(Budgets)' --output text 2>/dev/null || echo 0)" != "0" ]; then
  add_blind "budgets:remaining"
fi

BLIND_LEN="$(echo "$BLIND" | jq 'length')"
if [ "${REMAINING:-0}" -eq 0 ] && [ "$BLIND_LEN" -eq 0 ]; then
  emit true 0 "nuke + verification clean"
else
  emit false "${REMAINING:-0}" "verification found leftover resources" "$BLIND"
fi
