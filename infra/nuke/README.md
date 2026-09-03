# infra/nuke — Phase 3 2.2 nuke + verification

The containerised `aws-nuke` runner + mandatory verification pass, called
by `orchestrator/internal/cloudaws.RealClient.RunNuke` (production path)
and by `orchestrator/internal/cloudnuke.Sweeper` (nightly).

| file | what |
|---|---|
| `run.sh` | assume `PlatformNukeRole` → render `aws-nuke.yaml.tmpl` for the account → `aws-nuke run` → **verification pass** (AWS Config `select-resource-config` + Resource Explorer `search` + a hardcoded blind-spot list: Route53 hosted zones, leftover Budgets) → emit `{verified, resources_remaining, blind_spot_hits, detail}` JSON. Any non-empty result ⇒ `verified:false` ⇒ the pool manager QUARANTINEs and pages. |
| `aws-nuke.yaml.tmpl` | account-scoped `aws-nuke` config; `__ACCOUNT_ID__` substituted at runtime; filters keep `PlatformNukeRole` / `LearnerSandboxRole` / `practice-engine-*` roles and the OIDC provider. |

## Deploy

The orchestrator image must ship `aws`, `aws-nuke`, `jq`. `RealClient`
looks for the script at `CLOUD_NUKE_SCRIPT` (default
`/opt/practice/nuke/run.sh`) — mount this directory there, or bake it in.
Replace the placeholder `"000000000000"` in `aws-nuke.yaml.tmpl`'s
`account-blocklist` with the real payer account id.

Blocked on: a real AWS Organization + `PlatformNukeRole` in each sandbox
(Stage 1.1 / 1.3). Until then `cloudnuke.Sweeper` runs against
`cloudaws.FakeClient` and is unit-tested end-to-end.
