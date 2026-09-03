# Namespace-churn soak — 3× projected peak

Closes `PLAN_PHASE3_PROJECTS.md` **G2 / Phase3_Stages.md 0.3** (the soak _script_
+ pass criteria). Executing it on a real multi-node cluster and committing the
result is **0.4**, blocked on infra.

## The risk this exercises

`PLAN.md` R9 / `memory.md` §R9, §963, §2234:

> **etcd / K8s API saturation from namespace churn** — mitigation: "load test
> namespace churn at 3× projected peak before Phase 3". SLIs: API p99, etcd DB
> size, delete latency.

Namespace-per-environment means every provision creates a namespace (+ quota,
NetworkPolicy, RBAC, pod) and every destroy deletes it. At thousands of
create/delete per hour this stresses etcd. This soak drives that churn far
past steady-state and asserts the teardown path never leaks.

## Pass criteria (hard gate)

1. **Zero orphans during the run AND for 1 h after.**
   `increase(orchestrator_reaper_orphans_found_total[<window>]) == 0`, and no
   `env-*` namespace left in the cluster with no live `env.environment` row.
2. **Every provisioned environment is destroyed.** `destroyed == provisioned`
   at the end (the script tracks both; any gap is a leak).
3. **Reaper keeps up.** `orchestrator_reaper_destroyed_total` does not grow
   unbounded relative to explicit `Destroy` calls — i.e. the explicit
   teardown path, not the reaper's TTL backstop, is doing the work.
4. **API stays healthy.** K8s API p99 and etcd DB size stay within the
   headroom the operator records for the cluster (captured, not gated by this
   script — see `results-template.md`).

## What "3× projected peak" means here

`memory.md` §11.3 / §1882: shard at **~2,000 concurrent environments per
cluster**; §7.2 assumes **~60 concurrent T3** but T1/T2 practice load is the
churn driver at Phase 1/2. The soak is parameterised, not hardcoded to a
number — set `SOAK_PEAK` to the cluster's projected concurrent-environment
peak and the script runs at `3 × SOAK_PEAK` sustained churn:

| Var | Default | Meaning |
|---|---|---|
| `SOAK_PEAK` | `50` | projected concurrent-environment peak for the target cluster |
| `SOAK_MULTIPLIER` | `3` | R9's "3×" |
| `SOAK_DURATION_MIN` | `60` | churn phase length (minutes) |
| `SOAK_HOLD_MIN` | `60` | post-churn observation ("+ 1h after") |
| `SOAK_ENV_LIFETIME_SEC` | `45` | how long each env lives before Destroy — short, to maximise churn rate |
| `SOAK_TIER` | `T1` | `T1` or `T2` (0.5 runs this with `T2` for the microVM/CNI-leak check) |
| `SOAK_BLUEPRINT` | `bp.test.v1` | lightest blueprint (busybox); use `bp.linux.v1` for a realer pod |
| `SOAK_ORCH_ADDR` | `localhost:50051` | orchestrator gRPC |
| `SOAK_ORCH_METRICS_URL` | `http://localhost:9090` | orchestrator `/metrics` |
| `SOAK_SHARED_SECRET` | `compose-dev-shared-secret` | gRPC shared-secret auth |
| `SOAK_KUBECONFIG` | `.local/k3s-output/kubeconfig.yaml` | for the namespace cross-check + API-latency probe |

Target churn rate = `3 × SOAK_PEAK` environments alive at once, each cycling
every `SOAK_ENV_LIFETIME_SEC`, so provisions/hour ≈
`3 × SOAK_PEAK × 3600 / SOAK_ENV_LIFETIME_SEC`. With the defaults: 150
concurrent, ~12,000 provision+destroy pairs/hour.

## Why it drives the orchestrator directly (not practice-core)

The risk is K8s/etcd namespace churn, not the learner API. Going through
practice-core would hit its *1 active environment per learner* quota and its
throttler — irrelevant to R9 and a smaller churn ceiling. The soak calls
`EnvironmentOrchestrator/Provision` + `/Destroy` over gRPC with synthetic
`attempt_id`s, which is the exact code path a real provision takes below
practice-core.

## Run it

```
# against the compose stack (functional check only — single-node k3s can't
# sustain 150 concurrent pods; use SOAK_PEAK=3 SOAK_DURATION_MIN=5 locally)
SOAK_PEAK=3 SOAK_DURATION_MIN=5 SOAK_HOLD_MIN=5 \
  evaluation/phase1/soak/namespace-churn-soak.sh

# the real run (0.4), on a multi-node cluster
SOAK_PEAK=50 SOAK_DURATION_MIN=60 SOAK_HOLD_MIN=60 SOAK_TIER=T1 \
SOAK_ORCH_ADDR=orchestrator.staging.internal:50051 \
SOAK_ORCH_METRICS_URL=https://orchestrator.staging.internal/metrics \
  evaluation/phase1/soak/namespace-churn-soak.sh | tee soak-run.log

# 0.5: the same, against the T2 teardown path
SOAK_TIER=T2 ... evaluation/phase1/soak/namespace-churn-soak.sh
```

The script:

1. Records baseline metrics + etcd/API health.
2. Runs `SOAK_DURATION_MIN` of churn at `3 × SOAK_PEAK` concurrency
   (a fixed-size worker pool; each worker: Provision → wait lifetime →
   Destroy → repeat).
3. Stops issuing new provisions, drains outstanding Destroys.
4. Holds `SOAK_HOLD_MIN`, polling `reaper_orphans_found_total` the whole time.
5. Runs the namespace cross-check (`check-orphans.sh` logic, inlined).
6. Prints PASS/FAIL against criteria 1–3 and exits accordingly.

Capture the output into
`evaluation/phase1/results/soak-namespace-churn-<date>.md` via
`evaluation/phase1/soak/results-template.md`, then have a reviewer sign the
`PHASE2_TEARDOWN_SOAK.md` line (0.6).

## Reuses

`evaluation/phase1/load/check-orphans.sh` is the standalone orphan gate; this
soak inlines the same counter+namespace check so a single invocation is
self-contained. Run `check-orphans.sh` again 1 h later for an independent
second reading.
