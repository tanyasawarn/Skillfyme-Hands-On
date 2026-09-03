# infra/ — Phase 3 platform infrastructure

Infrastructure-as-code for Phase 3 (Projects + T3 Cloud Sandboxes),
`PLAN_PHASE3_PROJECTS.md` Track A + platform services.

| dir | Stage item | status |
|---|---|---|
| [`aws-org/`](aws-org/) | **1.1** AWS Organization + OU scaffold + centralised CloudTrail / Config / GuardDuty | authored; `tofu validate` clean; **apply `[B]`** (real payer account) |
| [`aws-org/scp/`](aws-org/scp/) | **1.2** SCP framework — 6 policy documents + a red-team verify script | policies authored + JSON-validated; **attach/verify `[B]`** (needs the Org) |
| [`account-baseline/`](account-baseline/) | **1.3** Account-baseline Terraform module — remote state, OIDC provider, required-tag enforcement | authored; `tofu validate` clean; **apply `[B]`** (real sandbox account) |
| [`git-hosting/`](git-hosting/) | **1.4** per-learner Forgejo | **local deploy works + verified**; production helm/tf authored, apply `[B]` |
| [`clickhouse/`](clickhouse/) | **1.5** ClickHouse cluster | local single-node **runs**; ClickHouse Cloud provisioning authored, apply `[B]` |

## Why so much is `[B]`

Every `tofu apply` against AWS needs real credentials for an AWS
Organizations **management (payer) account** — creating an Organization is
close to irreversible and the centralised CloudTrail/Config/GuardDuty +
account-vending cost real money (`memory.md` §7.2). The IaC here is
`tofu validate` + `tofu fmt` clean and plan-ready; `tofu plan` itself needs
credentials.

To apply (when a Platform AWS account exists):

```
# per module
cd infra/<module>
tofu init -backend-config=backend.hcl   # backend.hcl supplies the S3 state bucket (1.3 creates it first, bootstrapped with a local backend)
tofu plan  -var-file=env/prod.tfvars
tofu apply -var-file=env/prod.tfvars
```

## Toolchain

- **OpenTofu** ≥ 1.6 (`tofu`) or Terraform ≥ 1.6 — both are installed on
  this machine; the code is provider-config compatible with either.
- **AWS CLI v2** for the verify scripts.
