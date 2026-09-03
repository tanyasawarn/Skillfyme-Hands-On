# Quarantine runbook — T3 sandbox accounts

Phase 3 4.7. When a vended AWS sandbox account is moved to `QUARANTINED`,
on-call is paged (`webhookPager`, `PAGER_WEBHOOK_URL`). This is what to do.

## What quarantine means

`nuke + verify` disagreed, `aws-nuke` errored, or a budget breach left an
account in an unknown state. The account is **held out of the pool** —
`accountpool.Manager` never auto-returns a `QUARANTINED` account to
`AVAILABLE`. A human must clear it.

`env.cloud_account.quarantine_reason` is one of:

| reason | meaning | first move |
|---|---|---|
| `verification_nonempty` | post-nuke verification (Config + Resource Explorer + blind-spot list) still found resources | inspect the leftover resources; usually a blind-spot service |
| `nuke_error` | `aws-nuke` itself exited non-zero | read `quarantine_detail`; re-run `infra/nuke/run.sh` manually |
| `budget_breach_stuck` | 100% budget breach + a failed force-terminate | the compute may still be running; kill it, then nuke |
| `sweeper` | the nightly sweeper found resources in an account that should be empty | a real pool leak — treat as a §2 gate regression |
| `manual` | an operator quarantined it | see the ticket that prompted it |

## Investigate

```
export ACCOUNT=<aws_account_id>

# 1. what does the platform think happened
psql "$DATABASE_URL" -c "SELECT state, quarantine_reason, quarantine_resources_remaining,
                                quarantine_detail, attempt_id, region, last_nuked_at
                           FROM env.cloud_account WHERE aws_account_id = '$ACCOUNT'"

# 2. what is actually in the account (assume the nuke role)
aws sts assume-role --role-arn arn:aws:iam::$ACCOUNT:role/PlatformNukeRole \
  --role-session-name qtn-investigate --output json > /tmp/creds.json
export AWS_ACCESS_KEY_ID=$(jq -r .Credentials.AccessKeyId /tmp/creds.json)
export AWS_SECRET_ACCESS_KEY=$(jq -r .Credentials.SecretAccessKey /tmp/creds.json)
export AWS_SESSION_TOKEN=$(jq -r .Credentials.SessionToken /tmp/creds.json)

aws resource-explorer-2 search --query-string "*" --output json | jq '.Resources | length'
aws configservice select-resource-config \
  --expression "SELECT resourceId, resourceType WHERE configuration.state.value != 'terminated'"
# blind-spot manual checks
aws route53 list-hosted-zones
aws budgets describe-budgets --account-id $ACCOUNT
```

## Remediate

1. **Delete the leftover resources** (manually, or a targeted
   `infra/nuke/run.sh --account $ACCOUNT` after fixing the aws-nuke config
   if it's a config gap — then commit the config fix).
2. **Re-run the nightly sweeper against just this account** to confirm it
   verifies clean:
   ```
   # the sweeper's per-account path — or wait for the nightly run
   aws-nuke run --config <rendered> --no-dry-run --quiet
   infra/nuke/run.sh --account $ACCOUNT --platform-account $PLATFORM_ACCOUNT \
     --region $AWS_REGION --json
   # expect: {"verified": true, "resources_remaining": 0, ...}
   ```
3. **Return it to the pool** once clean:
   ```
   psql "$DATABASE_URL" -c "UPDATE env.cloud_account
        SET state='AVAILABLE', attempt_id=NULL, budget_usd=NULL,
            quarantine_reason=NULL, quarantine_resources_remaining=NULL,
            quarantine_detail=NULL, last_nuked_at=now(), updated_at=now()
      WHERE aws_account_id='$ACCOUNT' AND state='QUARANTINED'"
   ```
   The `accountpool` filler re-syncs Redis on its next tick (or restart
   the orchestrator).

## If it's a `sweeper` quarantine

A `sweeper` reason on an `AVAILABLE` account means the pool leaked — the
same class of bug the §2 orphan gate exists to catch. **Do not just clear
it.** File a regression: which resource type, which attempt last held the
account, whether the release path or the sweeper found it. The T3 build
gate (0.6) assumes zero pool leaks.

## Capacity note

Every quarantined account is one fewer in the pool. If the
`account_pool_depth` gauge drops below the region target while accounts
sit in quarantine, vend replacements ahead of the next cohort
(`aws organizations create-account` → `accountpool` `RegisterAvailableAccount`)
— account creation takes minutes, quota raises take weeks (§7.4).
