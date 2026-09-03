# t3-terraform — static fixture repo for the T3 validator executors

Phase 3 (PLAN_PHASE3_PROJECTS.md 1.8 / B3). Small Terraform trees used to
test `IAC_STATE` and `STATIC_ANALYSIS` executors through `LocalShellRunner`
(the de-risking path — a static repo instead of a live T3 env).

| dir | purpose |
|---|---|
| `clean/` | a well-formed config with a **local** backend and a checked-in state that is in sync (no drift). `null_resource` only — `terraform init && terraform apply` need no cloud creds. |
| `drifted/` | same as `clean/` but the checked-in `terraform.tfstate` describes a resource that the config no longer contains → `terraform plan -detailed-exitcode` exits 2. |
| `with-secrets/` | a state file containing an AKIA-shaped access key and a password field → `forbid_secrets_in_state` must FAIL. |
| `insecure/` | `aws_s3_bucket` with a public ACL and an unencrypted `aws_db_instance` → `tfsec` reports HIGH/CRITICAL findings. Not applied (no creds); STATIC_ANALYSIS scans the source, it does not need state. |

The `.tfstate` files are committed on purpose — they are test fixtures, not
real infrastructure state. `terraform`/`tofu` must be on PATH for the
IAC_STATE tests; `tfsec` for the STATIC_ANALYSIS test. Tests that need a
missing binary `skip` rather than fail (see `t3-validators.integration.spec.ts`).
