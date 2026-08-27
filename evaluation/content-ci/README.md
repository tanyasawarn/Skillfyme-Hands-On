# content-ci

The provision → null-path → golden-path → flake → timing → cost harness from doc §3.5,
running against a **real** k3s cluster + orchestrator. The schema/skill/DAG **lint** stage runs
in the main `ci.yml` on hosted runners; everything that needs to actually provision an
environment runs here.

## What runs where

| Stage | Where | Trigger |
|---|---|---|
| lint (schema, skill slugs, prereq DAG) | `ci.yml` → `content-lint` job, hosted runner | every PR |
| provision / null / golden / flake / timing / cost | **`content-ci.yml`**, self-hosted `content-ci` runner | nightly (full library) + per-PR (changed activities) + manual |

## The pipeline, per activity

`practice-core/scripts/content-ci.ts`, one environment per activity:

1. **provision** a fresh T1 environment from the activity's blueprint. Fail → `PROVISION FAILED`.
2. **null path** — run every task's validators against the untouched environment. Every
   validator must **FAIL** (or ERROR). A validator that PASSes here is too weak (it passes a
   learner who did nothing) → `NULL PATH FAIL`.
3. **golden path** — apply each task's `solution_apply` script via `ExecShell`, then re-run
   every validator. Every validator must **PASS** → otherwise `GOLDEN PATH FAIL` (the solution
   script or the validator is broken).
4. **flake** — repeat the golden-path validator run `CI_FLAKE_RUNS` times (default 5) without
   re-applying the solution. Any validator whose status differs across runs → `FLAKE DETECTED`.
5. **timing** — wall-clock, provision through the last flake run. Reported, not gated (yet).
6. **cost** — `elapsed_hours × $0.04` (T1 rate). Over `CI_BUDGET_USD` (default $0.08) →
   `COST OVER BUDGET`: verifying this activity already costs more than a whole learner attempt's
   budget.
7. **destroy** the environment (always, `finally`).

## Exit-code contract

`run-content-ci.sh` exits with `content-ci.ts`'s code:

| Code | Meaning |
|---|---|
| `0` | every **selected** activity passed all stages |
| `1` | at least one selected activity failed a stage, **or** an explicitly-named activity has no runnable golden path (no `reference_solution.repo_path`, or `solution_apply` scripts missing on disk), **or** a selector matched no file |

On a **full-library** run (no selectors), activities with no authored golden path are `SKIP`
(reported, not failed) — most of the library is still un-authored, tracked in
`PHASE0_1_2_PENDING_CLOSEOUT.md` Track 2C. On a **per-PR** or **manual** run, a named activity
with no golden path is a **FAIL** — you asked to verify it and it can't be verified.

## Reading a failure

The stage name is in the error line:

```
=== lab.docker.basics ===
  provisioned env=env-abc123
  null path OK: all 3 validators correctly FAIL/ERROR on untouched env
  GOLDEN PATH FAIL: validators did not pass after solution applied:
    v.image-exists: FAIL
  destroyed env=env-abc123
```

- `PROVISION FAILED` → blueprint / fixture problem, or the orchestrator can't reach k3s.
- `NULL PATH FAIL` → a validator is trivially satisfiable. Tighten its `expect`.
- `GOLDEN PATH FAIL` → the `solution_apply` script for that task doesn't actually produce the
  state the validator checks, or the validator is wrong. Run the script by hand in a provisioned
  env and inspect.
- `FLAKE DETECTED` → non-deterministic validator (timing, eventual consistency). Add a
  retry/backoff to the validator, or a settle step to the solution script.
- `COST OVER BUDGET` → the solution scripts or validators got slow, or the blueprint is heavy.

## Running it locally (docker-compose k3s)

```sh
# 1. bring up infra + k3s
docker compose --profile dev-a up -d postgres redis nats registry k3s minio minio-init

# 2. apply migrations (orchestrator env schema + practice-core)
for f in orchestrator/db/migrations/*.sql practice-core/db/migrations/*.sql; do
  PGPASSWORD=practice psql -h localhost -p 5433 -U practice -d practice_engine -v ON_ERROR_STOP=1 -f "$f"
done

# 3. run the orchestrator against that k3s
export KUBECONFIG=.local/k3s-output/kubeconfig.yaml
export DATABASE_URL=postgres://practice:practice@localhost:5433/practice_engine
export REDIS_URL=redis://localhost:6379 NATS_URL=nats://localhost:4222
export WS_GATEWAY_JWT_SECRET=dev ORCHESTRATOR_SHARED_SECRET=dev-secret
( cd orchestrator && go run ./cmd/orchestrator ) &

# 4. run content-ci against one activity
export DATABASE_URL ORCHESTRATOR_SHARED_SECRET
export ORCHESTRATOR_GRPC_ADDRESS=localhost:50051
bash scripts/ci/run-content-ci.sh lab.devops.fundamentals
```

## Results

Green-run evidence (first green, and ongoing nightly/per-PR confirmations) goes in
`results/` — see `PHASE0_1_2_PENDING_CLOSEOUT.md` items 2B.3 / 2B.4 / 2C.5.
