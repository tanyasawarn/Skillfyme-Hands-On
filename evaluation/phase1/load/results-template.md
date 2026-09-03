# Phase 1 load run — <YYYY-MM-DD>

> Copy to `evaluation/phase1/results/loadtest-<date>.md` and fill in. This
> committed artifact is what closes `Phase3_Stages.md` **0.4** and the
> `PHASE1_MVP_COMPLETION.md` §7 rows. Every number must come from a captured
> command / dashboard / query — asserted, not estimated (§7 heading).

## Environment

| | |
|---|---|
| Cluster | <provider, node count, instance type, k8s version> |
| practice-core | <image tag / commit> |
| orchestrator | <image tag / commit> |
| Runner | `load.js` via k6 <version>  /  `load_driver.py` (python <version>) |
| Command | `<exact command line, including all LOAD_* env vars>` |
| Start / end (UTC) | <t0> → <t1> |

## Scenario parameters

| Var | Value |
|---|---|
| `LOAD_VUS` | 200 |
| `LOAD_ITERATIONS` | 3 |
| `LOAD_CMDS_PER_ATTEMPT` | 20 |
| `LOAD_LAB_SLUG` | lab.linux.navigate-filesystem |

## Results vs doc §13.1 exit criteria

| Criterion | Threshold | Measured | Source | Pass? |
|---|---|---|---|---|
| ≥ 200 learners × ≥ 3 labs | 600 completions | | `labs_completed` counter | |
| Provision success | ≥ 99 % | | `provision_ok` rate / `provision_total{result=}` | |
| Time-to-ready p95 | ≤ 20 s | | `provision_duration_ms` p(95) — cross-check `orchestrator_provision_duration_seconds` histogram | |
| Submit success | ≥ 99 % | | `submit_ok` rate | |
| Teardown success | 100 % | | `destroy_ok` rate | |
| Validator ERROR rate | < 0.5 % | | practice-core validator metrics over the window | |
| Cost / attempt | < $0.08 | | `usage_meter` per-attempt aggregate (see `evaluation/phase1/README.md`) | |
| **Zero orphan environments (during + 1h after)** | `reaper_orphans_found_total` == 0 | | `check-orphans.sh` ×2 (below) | |
| Measured Elo available per lab | present | | export query + sample output | |

## `check-orphans.sh` output

### Run 1 — immediately after the load run

```
<paste the full check-orphans.sh output here>
```

### Run 2 — ≥ 1 h later

```
<paste the full check-orphans.sh output here>
```

## k6 / python summary

```
<paste the run summary (load-summary.json highlights or the python stdout block)>
```

## Notes / anomalies

- <e.g. provision p95 spiked at T+12m to 24s while the warm pool refilled; recovered by T+15m>
- <any threshold miss + the follow-up issue link>

## Sign-off

- Run by: <name>, <date>
- Reviewed by (not the implementer): <name>, <date>
