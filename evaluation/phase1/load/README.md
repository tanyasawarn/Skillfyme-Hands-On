# Phase 1 load harness — 200 concurrent learners

Closes `PLAN_PHASE3_PROJECTS.md` **G1 / Phase3_Stages.md 0.2** (the load-harness
_code_). It does **not** close the run itself — `PHASE1_MVP_COMPLETION.md` §7 rows
stay `[B]` until this is executed on a real multi-node cluster and
`evaluation/phase1/results/loadtest-<date>.md` is committed with the thresholds
met. That execution is Phase3_Stages.md 0.4, blocked on infra.

## What it drives

Per virtual learner, the full attempt lifecycle the doc §13.1 exit criteria
measure:

```
dev-login → pick a published L1 lab → create attempt → provision (real k3s pod)
          → connect (terminal WS URL) → start (→ IN_PROGRESS) → 20+ workspace
            file writes (stand-in for "20+ commands") → check (learner-triggered
            validation) → submit → destroy / teardown
```

- **200 concurrent** virtual learners (`LOAD_VUS`, default 200).
- Each completes **≥ 3 labs** (`LOAD_ITERATIONS`, default 3) — the §13.1
  "≥ 200 learners complete ≥ 3 labs each" row.
- **20+ commands** per attempt is modelled as 20 `POST .../files/content`
  writes. Phase 1 has no batch "run N shell commands" API on the learner REST
  surface; file writes exercise the same practice-core → workspace-pod path at
  the same cardinality. Set `LOAD_CMDS_PER_ATTEMPT` to change it.

## Two runners, one scenario

| File | Runner | Use |
|---|---|---|
| `load.js` | **k6** (`grafana/k6`) | The deliverable of record. Emits native k6 metrics + a JSON summary; thresholds encoded as `thresholds:` so the run self-grades. |
| `load_driver.py` | **python3 + stdlib only** | Runnable _now_ without installing k6 (matches `evaluation/phase1/smoke/run-smoke.sh`'s toolchain). Same scenario, same env vars, same pass/fail gate. Use for a smaller local shake-out (`LOAD_VUS=20`) before the real k6 run. |

Both read the **same environment variables** and target the same endpoints, so a
result from either is comparable.

## Environment variables

| Var | Default | Meaning |
|---|---|---|
| `LOAD_BASE_URL` | `http://localhost:3001` | practice-core base URL |
| `LOAD_ORCH_METRICS_URL` | `http://localhost:9090` | orchestrator `/metrics` (for the orphan check + provision-duration cross-read) |
| `LOAD_VUS` | `200` | concurrent virtual learners |
| `LOAD_ITERATIONS` | `3` | labs completed per learner |
| `LOAD_CMDS_PER_ATTEMPT` | `20` | file writes per attempt (the "20+ commands") |
| `LOAD_LAB_SLUG` | `lab.linux.navigate-filesystem` | published L1 lab to run (must be in the catalog) |
| `LOAD_SHARED_SECRET` | `compose-dev-shared-secret` | orchestrator shared secret, for the direct `Destroy` fallback |
| `LOAD_RAMP` | `30s` | k6 ramp-up to full VUs |
| `LOAD_THINK_MS` | `250` | pause between a learner's steps |

## Thresholds (doc §13.1, encoded in `load.js`)

| Metric | Threshold | k6 check |
|---|---|---|
| Provision success | ≥ 99 % | `rate{provision_ok} >= 0.99` |
| Time-to-ready p95 | ≤ 20 s | `p(95){provision_duration_ms} <= 20000` |
| Submit success | ≥ 99 % | `rate{submit_ok} >= 0.99` |
| Teardown success | 100 % | `rate{destroy_ok} == 1.0` |
| Orphan environments | 0 | asserted post-run by `check-orphans.sh` |

Validator ERROR rate (< 0.5 %) and cost/attempt (< $0.08) are read from
practice-core validator metrics and `usage_meter` respectively — see
`evaluation/phase1/README.md` for those queries; they are not gated inside the
load script.

## Run it

### k6 (the real run — needs the multi-node cluster, Phase3 0.4)

```
LOAD_BASE_URL=https://practice-core.staging.internal \
LOAD_ORCH_METRICS_URL=https://orchestrator.staging.internal/metrics \
LOAD_VUS=200 LOAD_ITERATIONS=3 \
k6 run --summary-export evaluation/phase1/results/loadtest-$(date +%Y%m%d)-k6-summary.json \
  evaluation/phase1/load/load.js
```

Then, immediately after and again 1 h later:

```
evaluation/phase1/load/check-orphans.sh   # gate: increase(reaper_orphans_found_total[1h]) == 0
```

Capture both `check-orphans.sh` outputs plus the k6 summary into
`evaluation/phase1/results/loadtest-<date>.md` using
`evaluation/phase1/load/results-template.md`.

### python3 (local shake-out — runnable today)

```
# small, against the compose `app` profile
LOAD_VUS=20 LOAD_ITERATIONS=2 python3 evaluation/phase1/load/load_driver.py
```

Exit code is non-zero if any threshold is missed, so it drops straight into CI
or a pre-flight check.

## What this harness deliberately does NOT do

- It does not stand up the cluster or the stack. Bring the system up first
  (`docker compose --profile app up -d --build`, or point the env vars at a
  real deployment).
- It does not seed prerequisite mastery for 200 users. The real run uses
  seeded load-test tenants; for the local python shake-out against compose,
  run `evaluation/phase1/load/seed-load-users.sh` once (it bulk-inserts
  `skill.skill_mastery` for `load-user-*`, same fixture stance as the smoke
  test's single-user seed).
- Single-node Docker-Desktop k3s **cannot** hold 200 concurrent workspace
  pods — a full-scale local run will (correctly) fail the provision-success
  threshold. That is why 0.4 is `[B]` on a real cluster.

## Real-run prerequisites (surfaced by the local shake-out)

Running the driver against the compose stack immediately exposed three gates a
200-VU run must clear. None are harness bugs — they're why 0.4 needs the
provisioned load-test fixtures:

1. **Distinct users, one env each.** practice-core enforces *1 active
   environment per learner* (`CONCURRENT_QUOTA_EXCEEDED`). With `dev-login`
   every VU is the same user, so only ~1 attempt provisions at a time. The
   real run needs **200 distinct load-test users**, each with its own token —
   see `LOAD_PREMINTED_TOKENS` below.
2. **Prerequisite mastery.** Each load-test user needs the target lab's
   `REQUIRES` closure seeded in `skill.skill_mastery` (else
   `PREREQUISITE_NOT_MET` on attempt create). `seed-load-users.sh` does this
   for the compose demo user; the real run seeds all 200 as part of fixture
   provisioning.
3. **dev-login throttling.** `POST /v1/auth/dev-login` is behind the global
   `@nestjs/throttler` guard — 200 VUs logging in per iteration trips
   `ThrottlerException: 429`. Mint tokens up front instead.

### `LOAD_PREMINTED_TOKENS`

One JWT per line, indexed by VU (`__VU-1 % N` in k6, `vu % N` in python). When
set, both runners use a line from this file instead of calling `dev-login` —
so no throttler contention and each VU can carry a distinct user.

Producing the file is part of load-test fixture provisioning for the target
deployment (0.4), not this harness's job: create N load-test users + seed
their prerequisite mastery, then mint one token per user with whatever the
deployment's auth path is (a signed JWT with that `userId`/`tenantId`; the
`JWT_SECRET` and claim shape are in `practice-core/scripts/mint-dev-token.ts`,
which today hardcodes the single demo user — generalise it or sign directly).
Then:

```
LOAD_PREMINTED_TOKENS=/path/to/load-tokens.txt LOAD_VUS=200 \
  k6 run evaluation/phase1/load/load.js
```

The local `LOAD_VUS≤2` shake-out without preminted tokens is expected to fail
the thresholds on gates 1–3 above — it only proves the lifecycle wiring
(provision → connect → start → writes → check → submit → teardown all reach
the real orchestrator and a real k3s pod).
