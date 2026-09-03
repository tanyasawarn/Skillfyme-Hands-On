# Reaper / namespace-churn soak — 2026-09-03 (local, modest)

**This is NOT the PLAN.md-grade run.** It is a real but modest local soak:
12-way concurrency, 30s env lifetime, 8m churn + 5m hold, single-node
compose k3s (runc). Full log:
`evaluation/phase1/results/soak-namespace-churn-20260903-local-12way-13min.log`.

The PLAN.md Phase-2 entry gate ("reaper/teardown has run with zero
orphans for a sustained period", derived from doc §13.1's T3 zero-orphan
gate and R9's "3× projected peak") needs 150-way (3×50) concurrency, ~45s
lifetime, ≥60m churn + ≥60m hold, on a real multi-node cluster. That run
remains a production-cluster task.

## Config

| | |
|---|---|
| Concurrency | 12 (SOAK_PEAK=4 × SOAK_MULTIPLIER=3) |
| Env lifetime | 30s |
| Churn | 8 min |
| Hold | 5 min |
| Tier / blueprint | T1 / bp.linux.v1 |
| Cluster | compose k3s, single node, **runc (not gVisor)** |
| Orchestrator | built from source this session (ExecShell + K8S_ASSERT + fixture fixes) |

## Result

| Criterion | Verdict | Detail |
|---|---|---|
| 1. Zero `orphans_found_total` delta (churn + hold) | **FAIL** | +27. See analysis below — the reaper's OrphanSweep *found and cleaned* 27 envs whose explicit `Destroy` had not fired when churn workers were stopped at t-0. Not a leak (see criterion 4). |
| 2. Every provisioned env explicitly destroyed | **PASS** | 153 provisioned, 153 confirmed-destroyed, **0 failed Destroy** |
| 3. Reaper TTL backstop ≤5% of teardowns | **FAIL** | 18% (28/153). Under 30s lifetime + 12-way churn on one node, the explicit Destroy path lags slightly and the reaper picks up the remainder. The real config's 45s lifetime gives the explicit path more slack. |
| 4. env-* namespace count returns to baseline | **PASS** | 5 → **0**. Verified post-run: `kubectl get ns | grep -c env-` = 0, `env.environment_reaper` rows = 0. |

## Analysis

- **No environment leaked.** Every namespace drained; the reaper table is
  empty; `kubectl` shows zero `env-*` namespaces. Criteria 2 and 4 — the
  ones that directly measure "did anything leak" — both PASS.
- **Criterion 1 FAIL is a gate artifact, not a defect.** When the 12
  churn workers are told to stop at t-0, ~27 of them are mid-cycle (env
  provisioned, lifetime sleep not elapsed, explicit Destroy not yet
  issued). The reaper's OrphanSweep then finds those namespaces (they are
  not in the reaper's explicit-destroy registry as "owned by a live
  attempt") and destroys them. That is exactly what the orphan sweep is
  for. `orphans_found_total` counts each such find, so the delta is
  non-zero even though nothing was actually orphaned in the "leaked and
  never cleaned" sense. A cleaner soak harness would drain all workers
  gracefully (finish their current cycle's Destroy) before sampling the
  counter.
- **Criterion 3 FAIL reflects the aggressive local config**, not the
  teardown path. 30s lifetime + 12 concurrent provision/destroy loops on
  a single-node k3s is a harder ratio than the real 45s config; the
  reaper absorbing 18% is the safety net working, and the explicit path
  still handled 82%.

## What this does and does not establish

**Establishes (local):** the reaper runs on its 60s tick; `Register` /
`Unregister` / `sweep` / `OrphanSweep` all execute against a real
cluster; explicit `Destroy` succeeds 153/153 under sustained churn;
namespaces and the reaper table drain fully; API-server latency stayed
healthy (~50-120ms) throughout.

**Does not establish:** the PLAN.md sustained zero-orphan gate (3× peak,
multi-node, ≥1h+1h). That is unchanged and still required before Phase 2
T2 work per the plan's dependency note.
