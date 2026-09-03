# Phase 1 — local (₹0) verification summary

**Scope of this pass:** Areas 1–2 of the Phase 1 close-out (content
solutions + content CI), plus one pre-existing test failure fixed.
Everything here was done **locally with zero cloud spend**: `docker
compose` (Postgres/Redis/NATS/k3s/MinIO/registry) + the orchestrator
built from source. No `tofu apply`, no billable cloud API calls, no faked
evidence.

**Not done this pass** (explicitly deferred): real AuthProvider/LTI seam,
IP1–IP4 integration round-trips, Monaco/workspace, gRPC client, recording
/logging, security-baseline runtime, exit-criteria harness. Those remain
open Phase 1 work.

---

## LOCAL VERIFIED

| Item | Evidence |
|---|---|
| **Pre-existing test failure fixed** — `eligibility.service.ts` retry-cooldown check (doc §2.7) had been commented out with a `// TESTING:` note and never restored. Re-enabled with a Date-normalised comparison. | `practice-core` integration suite: **115 pass / 0 fail / 7 skipped** (was 114 / 1 / 7). `attempt-lifecycle.integration.spec.ts`: **20/20**. Unit: **210/210**. Zero regressions. |
| **`content-ci.ts` now forwards seed fixtures** — it was ignoring `spec.environment.seed[].fixture` and provisioning every lab bare, so no fixture-dependent lab could ever pass golden-path. Now maps them to `ProvisionRequest.fixtures`. | `practice-core/scripts/content-ci.ts` diff; `seed fixtures: …` lines now appear in run logs. |
| **Content lint — 74/74 PASS** (63 labs + 11 sims): JSON-Schema + skill-existence + prerequisite-DAG. | `evaluation/phase1/results/logs/content-lint-*.log` |
| **`solution_apply` now exists for 63/63 labs** (was 13). | `content/activities/solutions/` — 63 dirs, 83 `*_apply.sh` scripts. |
| **18 labs — golden-path + flake VERIFIED PASS locally** via the production validator executors: `linux.navigate-filesystem`, `devops.fundamentals`, `devops.gitops-evolution`, `iac.fundamentals`, `iac.terraform-vs-cloudformation`, `microservices.devops-impact`, `microservices.fundamentals`, `observability.nagios-alerting`, `sre.write-a-postmortem`, `ansible.basics`, `terraform.modules-workspaces`, `cicd.troubleshooting`, `github.actions-workflows`, `gitlab.cicd-pipelines`, `jenkins.pipeline-as-code`, `jenkins.advanced-pipelines`, `jenkins.distributed-builds`, `jenkins.security-integration`, `jenkins.basics`, `devsecops.fundamentals`. (20 listed — `devsecops.fundamentals` + `microservices.fundamentals` pass golden-path + flake but have a weak *null-path* validator, a content bug, so overall content-ci marks the batch FAIL. Their solutions are correct.) | `evaluation/phase1/results/logs/content-ci-verified-pass-*.log` — every one shows `golden path OK … flake check OK … across 3 runs`. |
| **Full-library content-CI run executed** (all 74 activities) — first time a whole-library run has been driven end-to-end against a live orchestrator. | `evaluation/phase1/results/logs/content-ci-full-library-*.log` |
| **Content completion matrix** — per-lab `solution_apply_exists / solution_execution / validator_result / status` with an exact BLOCKED reason for every non-PASS. | `evaluation/phase1/results/content-completion-matrix.md` |

---

## REAL CLUSTER REQUIRED / BLOCKED

| Blocked item | Why | What clears it |
|---|---|---|
| **45 of 63 labs — golden-path not verifiable locally** | Local workspace image `linux-tools:v1` lacks `docker`/`terraform`/`helm`/`python3`/`ansible` and the pod has no egress (8 labs); 27 referenced `fx.*` fixtures have no handler (20 labs); learner RBAC excludes `nodes`/`resourcequotas`/`hpa`/CRDs (5 labs); `K8S_ASSERT` executor can't read istio/argo/tekton CRDs or `kubectl exec` (6, overlap); `ExecShell` doesn't honour the gRPC deadline and its exit-marker parse is fragile, so `kubectl`-driven solution scripts hang or error (8 k8s labs). | Per-item fixes listed in the matrix's "What unblocks the rest". None require gVisor — they require workspace-image tooling, fixture handlers, an RBAC decision, executor coverage, and an `ExecShell` robustness fix. |
| **gVisor isolation of T1 workloads** | Local k3s runs `runc`. `ORCHESTRATOR_GVISOR_ENABLED=false` locally. | The regional GKE-Sandbox cluster (`infra/practice-cluster/gke/`) — blocked on a GCP billing account (`open: false`). |
| **Content CI green on the full library** | Depends on all of the above. This pass got a full run *executed* and 18 labs *green*; a fully-green library needs the unblock work. | Same. |
| **Per-PR content CI** | Same runner + solution-set dependency. | Same. |
| The 7 Phase 1 exit-criteria, 200-learner load, real auth, real S3, multi-node | Out of scope for this pass; unchanged. | Real cluster + the other Phase 1 areas. |

---

## Files changed this pass

**Modified (tracked):**
- `practice-core/src/modules/attempt/eligibility.service.ts` — restore retry-cooldown eligibility check
- `practice-core/scripts/content-ci.ts` — forward `spec.environment.seed` fixtures to `Provision`; add `SeedRef` type
- `content/activities/lab.sre.write-a-postmortem.yaml` — add `solution_apply: scripts/t1_apply.sh` to t1 (it had none, so content-CI had no runnable golden path)

**Added:**
- 46 new `content/activities/solutions/lab.*/` dirs (63 total now) — 79 new `*_apply.sh` scripts. 18 are verified-PASS end-state solutions; the rest are honest end-state references or `exit 1` BLOCKED stubs that name their blocker (never a fake pass).
- `evaluation/phase1/results/content-completion-matrix.md`
- `evaluation/phase1/results/phase1-local-verification.md` (this file)
- `evaluation/phase1/results/logs/content-lint-*.log`, `content-ci-full-library-*.log`, `content-ci-verified-pass-*.log`

**Not touched:** orchestrator Go, RBAC, `ExecShell`, contracts, web — per the "no orchestrator changes / fix real local failures only" instruction.

---

## Commands (reproducible)

```bash
# local stack
open -a "Docker Desktop"
docker compose --profile dev-a up -d            # clean single-node k3s + registry + minio
docker compose --profile app up -d --build orchestrator

# test DB
PGPASSWORD=practice psql -h localhost -p 5433 -U practice -d postgres -c "CREATE DATABASE practice_engine_test"
for f in practice-core/db/migrations/*.sql; do PGPASSWORD=practice psql -h localhost -p 5433 -U practice -d practice_engine_test -f "$f"; done

# the cooldown fix — regression check
cd practice-core
TEST_DATABASE_URL=postgres://practice:practice@localhost:5433/practice_engine_test NATS_URL=nats://localhost:4222 REDIS_URL=redis://localhost:6379 \
  npm test && npm run test:integration

# content lint (74/74)
DATABASE_URL=postgres://practice:practice@localhost:5433/practice_engine \
  npx ts-node -r tsconfig-paths/register scripts/lint-content.ts

# content CI — one lab, or full library (no args)
DATABASE_URL=postgres://practice:practice@localhost:5433/practice_engine \
ORCHESTRATOR_GRPC_ADDRESS=localhost:50051 ORCHESTRATOR_SHARED_SECRET=compose-dev-shared-secret \
  npx ts-node -r tsconfig-paths/register scripts/content-ci.ts lab.jenkins.basics
```
