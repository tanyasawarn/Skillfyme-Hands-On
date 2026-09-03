# Namespace-churn soak — <YYYY-MM-DD> (<T1|T2>)

> Copy to `evaluation/phase1/results/soak-namespace-churn-<date>.md` (T1) or
> `soak-namespace-churn-t2-<date>.md` (T2, closes 0.5). This artifact + a
> reviewer signature closes `Phase3_Stages.md` 0.3/0.4/0.5 and feeds the
> `PHASE2_TEARDOWN_SOAK.md` sign-off (0.6).

## Environment

| | |
|---|---|
| Cluster | <provider, node count, instance type, k8s version, etcd topology> |
| orchestrator | <image tag / commit> |
| Tier under test | <T1 shared-container / T2 isolated-microVM> |
| Blueprint | <bp.test.v1 / bp.linux.v1 / …> |
| Command | `<exact command line incl. all SOAK_* env vars>` |
| Start / end (UTC) | <t0> → <t1> |

## Parameters

| Var | Value | Derived |
|---|---|---|
| `SOAK_PEAK` | | concurrency = PEAK × MULTIPLIER = **<N>** |
| `SOAK_MULTIPLIER` | 3 | |
| `SOAK_ENV_LIFETIME_SEC` | | ≈ **<N>** provision+destroy pairs/hour |
| `SOAK_DURATION_MIN` | 60 | |
| `SOAK_HOLD_MIN` | 60 | the "+ 1h after" |

## Pass criteria (from the script's gate)

| # | Criterion | Result | Value |
|---|---|---|---|
| 1 | `increase(reaper_orphans_found_total)` == 0 during churn **and** the 1 h hold | PASS / FAIL | delta = <n>, hold max = <n> |
| 2 | every provisioned env explicitly destroyed (0 failed Destroy) | PASS / FAIL | prov <n> / dest <n> / failedD <n> |
| 3 | reaper TTL backstop handled ≤ 5 % of teardowns | PASS / FAIL | <pct>% (<reaper_delta>/<total_prov>) |
| 4 | `env-*` namespace count returned to baseline | PASS / FAIL | <base> → <final> |

## SLIs captured (criterion 4 in README — recorded, operator-judged)

| SLI | Baseline | Peak during churn | End of hold | Headroom limit |
|---|---|---|---|---|
| K8s API `/readyz` latency | <ms> | <ms> | <ms> | <ms> |
| etcd DB size | <MB> | <MB> | <MB> | <MB> |
| etcd `delete` p99 (apiserver_request_duration…) | <ms> | <ms> | <ms> | <ms> |
| apiserver 5xx rate | | | | |

> Pull these from the cluster's own monitoring (Prometheus / the
> `evaluation/phase1/observability/` dashboards); the soak script only samples
> `/readyz` round-trip as a cheap proxy.

## Full script output

```
<paste the entire namespace-churn-soak.sh stdout, including the per-30s churn
lines and the per-minute hold lines>
```

## Independent second orphan reading (≥ 1 h later)

```
<paste evaluation/phase1/load/check-orphans.sh output run ≥1h after the soak ended>
```

## Anomalies / notes

- <e.g. 3 Provision failures at T+22m coincident with a warm-pool refill; no orphan resulted>
- <T2 only: confirm no leftover Firecracker/Kata microVM processes and no CNI veth/bridge leak on any node — `ip link` / `ls /var/lib/firecracker` sweep result>

## Sign-off

- Run by: <name>, <date>
- Reviewed by (not the implementer): <name>, <date>
- `PHASE2_TEARDOWN_SOAK.md` updated: <yes/no>, <link>
