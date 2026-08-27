# Phase 1 Observability & Exit-Criteria Measurement

Everything needed to *measure* the doc §13.1 / `PHASE1_MVP_COMPLETION.md` §7
exit criteria. The instrumentation ships in the services; this directory is
the config to point a monitoring stack at them plus the queries that read
each criterion off.

## What's instrumented

| Where | Package | Endpoint |
|---|---|---|
| Orchestrator (Go) | `orchestrator/internal/metrics` | `:$ORCHESTRATOR_METRICS_PORT/metrics` (default `:9090`), also `/healthz` |
| Practice Core (Nest) | `practice-core/src/modules/metrics` | `GET /metrics` on the API server (default `:3001`) |

Both are Prometheus text-exposition format. The orchestrator serves metrics
on a **separate port** from the gRPC and WS data planes so scraping never
contends with learner traffic.

## Files

- **`prometheus.yml`** — scrape config + `rule_files: [alerts.yml]`. Edit the
  target hosts for your deployment.
- **`alerts.yml`** — one alert per exit criterion, thresholds = the doc's
  numbers. `phase1-exit-criteria` group is the gate; `phase1-operational`
  is early-warning.
- **`dashboard.json`** — Grafana dashboard, one panel per criterion.

## Reading each exit criterion

| Criterion (doc §13.1) | PromQL |
|---|---|
| Time-to-ready **p95 ≤ 20s** | `histogram_quantile(0.95, sum(rate(orchestrator_provision_duration_seconds_bucket[10m])) by (le))` |
| Provision success **≥ 99%** | `sum(rate(orchestrator_provision_total{result="success"}[15m])) / sum(rate(orchestrator_provision_total[15m]))` |
| Validator ERROR rate **< 0.5%** | `sum(rate(practice_core_validator_result_total{status="ERROR"}[15m])) / sum(rate(practice_core_validator_result_total[15m]))` |
| Cost/attempt **< $0.08** | `histogram_quantile(0.90, sum(rate(orchestrator_attempt_cost_usd_bucket[1h])) by (le))` — the histogram has a bucket boundary at exactly `0.08`, so `orchestrator_attempt_cost_usd_bucket{le="0.08"} / orchestrator_attempt_cost_usd_count` is the exact fraction of attempts under budget |
| **Zero orphan environments** | `increase(orchestrator_reaper_orphans_found_total[1h]) == 0` (sustained during the run **and** 1h after) |
| **≥ 200 active learners** | not a service metric — comes from the load harness (`evaluation/phase1/load/`, `[B]` in the checklist); cross-check against `orchestrator_ws_sessions_active` |
| Measured Elo per lab | `practice_core` scoring/Elo export — query TBD with the load-run data |

## Local bring-up

```sh
# 1. infra + orchestrator + practice-core (see docker-compose / §1.1 of the checklist)
# 2. Prometheus + Grafana:
docker run -d --name prom -p 9091:9090 \
  -v "$PWD/evaluation/phase1/observability/prometheus.yml:/etc/prometheus/prometheus.yml" \
  -v "$PWD/evaluation/phase1/observability/alerts.yml:/etc/prometheus/alerts.yml" \
  prom/prometheus
docker run -d --name grafana -p 3005:3000 grafana/grafana
# add Prometheus (http://host.docker.internal:9091) as a datasource, import dashboard.json
```

## Status

- [x] Orchestrator `/metrics` + `/healthz` — `orchestrator/internal/metrics`, wired in `cmd/orchestrator/main.go`
- [x] Practice Core `/metrics` — `practice-core/src/modules/metrics`, `MetricsModule` in `app.module.ts`
- [x] Instrumented: provision duration/outcome, warm-pool depth/claims, reaper destroys + orphans, idle destroys, budget actions, per-attempt + final attempt cost, WS sessions/connections, attempt transitions, validator results
- [x] Alert rules + Grafana dashboard committed
- [ ] **[operator]** stand the monitoring stack up against a production-like cluster and confirm the rules evaluate (`[B]` — needs the cluster)
- [ ] **[operator]** capture the exit-criteria values during a 200-learner load run → `evaluation/phase1/results/`
