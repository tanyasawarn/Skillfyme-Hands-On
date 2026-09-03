#!/usr/bin/env bash
# Phase 3 1.2 — SCP red-team verify. Assumes a sandbox-account role and
# asserts each denied action returns AccessDenied. Run AFTER the SCPs are
# attached (blocked on 1.1 + the apply).
#
#   SANDBOX_ROLE_ARN=arn:aws:iam::<sandbox-acct>:role/LearnerSandboxRole \
#   ALLOWED_REGION=us-east-1 DENIED_REGION=eu-west-1 \
#     infra/aws-org/scp/verify/red-team.sh
#
# Exit 0 iff every check below is denied (or, for the allowed-region
# control, permitted). Any check that unexpectedly SUCCEEDS is a hole in
# the SCP set and fails the script.
set -euo pipefail

ROLE_ARN="${SANDBOX_ROLE_ARN:?set SANDBOX_ROLE_ARN}"
ALLOWED_REGION="${ALLOWED_REGION:-us-east-1}"
DENIED_REGION="${DENIED_REGION:-eu-west-1}"
NUKE_ROLE="${NUKE_ROLE:-PlatformNukeRole}"

log()  { printf '\n\033[1;34m== %s\033[0m\n' "$*"; }
pass() { printf '\033[1;32m  PASS: %s\033[0m\n' "$*"; }
fail() { printf '\033[1;31m  FAIL: %s\033[0m\n' "$*" >&2; FAILURES=$((FAILURES+1)); }
FAILURES=0

log "assuming ${ROLE_ARN}"
CREDS_JSON="$(aws sts assume-role --role-arn "$ROLE_ARN" --role-session-name scp-redteam --output json)"
export AWS_ACCESS_KEY_ID="$(echo "$CREDS_JSON" | python3 -c 'import sys,json;print(json.load(sys.stdin)["Credentials"]["AccessKeyId"])')"
export AWS_SECRET_ACCESS_KEY="$(echo "$CREDS_JSON" | python3 -c 'import sys,json;print(json.load(sys.stdin)["Credentials"]["SecretAccessKey"])')"
export AWS_SESSION_TOKEN="$(echo "$CREDS_JSON" | python3 -c 'import sys,json;print(json.load(sys.stdin)["Credentials"]["SessionToken"])')"

# assert_denied "<label>" <command...>
assert_denied() {
  local label="$1"; shift
  if OUT="$("$@" 2>&1)"; then
    fail "$label — command SUCCEEDED (should be denied)"
  elif echo "$OUT" | grep -qiE 'AccessDenied|explicit deny|not authorized|UnauthorizedOperation'; then
    pass "$label — denied"
  else
    fail "$label — failed but not with AccessDenied: $(echo "$OUT" | head -1)"
  fi
}

assert_allowed() {
  local label="$1"; shift
  if "$@" >/dev/null 2>&1; then
    pass "$label — permitted (control)"
  else
    fail "$label — control command failed"
  fi
}

log "01 region-deny"
assert_denied "ec2 run-instances in a denied region" \
  aws ec2 run-instances --region "$DENIED_REGION" --image-id ami-00000000 --instance-type t3.micro --dry-run
assert_allowed "sts get-caller-identity (global — control)" \
  aws sts get-caller-identity

log "02 expensive-sku-deny"
assert_denied "ec2 run-instances p4d.24xlarge (no exception tag)" \
  aws ec2 run-instances --region "$ALLOWED_REGION" --image-id ami-00000000 --instance-type p4d.24xlarge --dry-run

log "03 org-boundary-deny"
assert_denied "organizations:LeaveOrganization" \
  aws organizations leave-organization
assert_denied "cloudtrail:StopLogging" \
  aws cloudtrail stop-logging --name practice-engine-org-trail
assert_denied "iam:DeleteRole on ${NUKE_ROLE}" \
  aws iam delete-role --role-name "$NUKE_ROLE"

log "04 iam-hardening-deny"
assert_denied "iam:CreateUser" \
  aws iam create-user --user-name scp-redteam-probe
assert_denied "iam:CreateAccessKey" \
  aws iam create-access-key --user-name irrelevant

log "05 public-sharing-deny"
assert_denied "s3api put-bucket-acl --acl public-read" \
  aws s3api put-bucket-acl --bucket does-not-matter --acl public-read

log "06 mail-abuse-deny"
assert_denied "sns:Publish (no messaging-exception tag)" \
  aws sns publish --topic-arn "arn:aws:sns:${ALLOWED_REGION}:000000000000:x" --message hi
assert_denied "ses:SendEmail" \
  aws ses send-email --from a@b.c --destination ToAddresses=d@e.f --message 'Subject={Data=x},Body={Text={Data=y}}' --region "$ALLOWED_REGION"

echo
if [ "$FAILURES" -eq 0 ]; then
  printf '\033[1;32mALL SCP CHECKS PASSED — every denied action returned AccessDenied\033[0m\n'
  exit 0
else
  printf '\033[1;31m%d SCP CHECK(S) FAILED — the policy set has a hole\033[0m\n' "$FAILURES" >&2
  exit 1
fi
