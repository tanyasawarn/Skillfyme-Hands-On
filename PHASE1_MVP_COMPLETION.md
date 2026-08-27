# Phase 1 — MVP Completion: End-to-End TODO Checklist & Completion Gate

**Owner:** (fill in) · **Created:** 2026-08-27 · **Status:** OPEN — implementation in progress
**Last update:** 2026-08-27 — §1.1 compose `app` profile + E2E smoke, §4.2 structured logging, and §5's
frontend identity-from-JWT delivered and verified (see notes inline). Phase 1 is NOT closable yet: §1.2
(≈60 reference solutions), §1.3/§3.3 content-CI-green, §7 (the 200-learner load run + its 7 measured
exit-criteria artifacts), and the backend half of §5 (AuthProvider seam + LTI provider + dev-login
off-by-default) remain open.
**Scope reference:** `PLAN.md` Phase 1 (T1 guided labs, single DevOps track, 25–35 labs, evidence→score→mastery
pipeline, static hints, rules-only recommendation) and `memory.md` (architecture blueprint) §13.1 exit criteria.

This file is the single hard gate for declaring Phase 1 **CLOSED**. No item is checked off from a doc comment,
a prior session's claim, or code that merely *describes* intent — only from direct verification against
current file state (`grep`/`Read`), a passing test run, or a captured measurement artifact committed to
`evaluation/phase1/`.

---

## 0. Legend & conventions

| Mark | Meaning |
|---|---|
| `[ ]` | not started |
| `[~]` | in progress |
| `[x]` | done + verified (verification note required) |
| `[B]` | **blocked on infrastructure/budget not available in the dev session** — needs a real cluster + load budget |

**Verification note format:** every `[x]` gets a one-line ` — verified: <how>` suffix (command run, file:line, or
artifact path).

**Blocked items** (`[B]`) are real Phase-1 exit requirements that a coding agent cannot execute from a laptop
session: they require a k8s cluster with a gVisor node pool, real compute budget, and a load-generation run.
The *harness and measurement tooling* for each is in scope and will be delivered; the *execution + captured
numbers* are the operator's step. Phase 1 cannot be CLOSED until these are done and their artifacts committed.

---

## 1. Feature completion

### 1.1 Frontend workspace ↔ orchestrator as the default execution path

**Audit result (2026-08-27):** the wiring already exists on the default path —
`practice-core/src/modules/attempt/attempt.module.ts` binds the **real** `GrpcOrchestratorClient` unless
`USE_FAKE_ORCHESTRATOR=true`; `orchestrator` `Server.Connect()` builds a real `TerminalWsUrl` from
`WS_GATEWAY_BASE_URL`; `web/src/components/WorkspaceTerminal.tsx` consumes `info.terminalWsUrl`. No
`fake-gateway.invalid` on the default path. The remaining gaps are packaging + verification:

- [x] `docker-compose.yml` runs `orchestrator`, `practice-core`, and `web` as services under an `app`
      profile. New `Dockerfile` at each service root (`orchestrator/Dockerfile` — multi-stage Go →
      distroless-static; `practice-core/Dockerfile` — Nest build → `node:22-slim` + tini, `contracts/` &
      `content/` bind-mounted one level above WORKDIR to satisfy the runtime `path.resolve(cwd,'..',…)`
      loader; `web/Dockerfile` — Next `output:'standalone'` → slim runtime). Compose additions:
      `db-migrate-orchestrator` one-shot applies Dev A's `env`/`billing` migrations (idempotent
      `CREATE … IF NOT EXISTS`); `kubeconfig-internal` one-shot rewrites the k3s kubeconfig `server:` to
      `https://k3s:6443`; `k3s` command gains `--tls-san=k3s`. — verified: `docker compose --profile app up`
      brings all 9 containers healthy.
- [x] End-to-end smoke test — `evaluation/phase1/smoke/run-smoke.sh` (+ `docker-compose.smoke.yml` port
      shift so it runs alongside local dev processes, + `README.md`). Proves: compose up → dev-login →
      start attempt on `lab.linux.navigate-filesystem` → `provision` (practice-core `GrpcOrchestratorClient`
      → orchestrator gRPC, shared-secret auth ON → **real k3s** cold-provision) → attempt `READY` →
      `kubectl get pod workspace -n env-<id>` is `Running` → `/connect` returns a real
      `ws://…/terminal?session=<signed JWT>` → orchestrator `Destroy` RPC → namespace gone. — verified:
      exit 0; result committed at `evaluation/phase1/results/smoke-compose-app-20260827.md`.
- [x] `web` `DEMO_USER_ID`/`DEMO_TENANT_ID` removed from every page. New `web/src/lib/session.ts`
      (`useSession()`) decodes `userId`/`tenantId`/`role` from the bearer JWT that `auth-token.ts` already
      obtains; 7 call sites (`page`, `history`, `skills`, `catalog`, `catalog/[id]`, `attempts/[id]`) now
      gate their queries on `!!session` and call the API as the token's subject. `demo-context.ts` keeps
      only `DEFAULT_COURSE_SLUG` (a launch-URL param, not identity). — verified: `tsc` clean, `npm test`
      95/95, `npm run build` clean, and the smoke run exercises the JWT-identity path end-to-end.
      NOTE: token issuer is still `POST /v1/auth/dev-login`; swapping it for LTI is the backend half of §5.
- [ ] Confirm `GrpcOrchestratorClient` per-RPC deadlines + retry policy (see §6) are set for `Connect`.
- [ ] `WorkspaceEditor` (Monaco) file operations go through a real orchestrator file API, not a stub — verify
      `WorkspaceFileService` path.

### 1.2 Content: 25–35 guided labs, single DevOps track
- [ ] Confirm ≥25 T1 guided labs (L1–L3) for the DevOps track validate + provision + golden-path + null-path
      via `practice-cli` (content CI). Current count: 63 `lab.*` (all `mode: GUIDED_LAB`, all
      `tier: SHARED_CONTAINER`) + 9 `sim.*` specs in `content/activities/`. 71 specs carry inline
      `solution_apply:` scripts (golden-path CI has something to run) but see next item.
- [ ] Every Phase-1 lab has: authored static hint ladder, scoring profile `sp.guided-lab.default`, at least one
      typed validator (`SHELL_ASSERT`/`SHELL_JSON`/`FILE_EXISTS`/`FILE_CONTENT`/`FILE_PARSE`/`K8S_ASSERT`).
- [ ] `content/activities/solutions/<id>/` reference-solution artifact for every Phase-1 lab. **STILL OPEN:**
      all 63 lab specs reference `reference_solution.repo_path: solutions/<id>/` but only **2 directories
      exist** (`lab.devops.fundamentals`, `lab.linux.navigate-filesystem`). This is ≈60 hand-authored
      solution sets, each of which must actually make its validators pass against a real provisioned env —
      genuine content-authoring work, explicitly out of scope for this session per the owner's direction.
- [ ] Content CI runs flake ×5 + timing + cost budget check per lab and is green. Gated on the
      reference-solution backlog above; the harness itself (`practice-cli`, `content-ci.ts` with the
      flake/timing/cost stages) is in place and runs against the live k3s.

### 1.3 Evidence → scoring → mastery pipeline
- [ ] Validator Runner executes all Phase-1 validator types against a real env with minted short-lived creds
      (`MintValidatorCredentials` RPC) — verify not stubbed.
- [ ] Scoring engine: signals→criteria→profile produces a score for a completed attempt end-to-end.
- [ ] BKT mastery engine: evidence update + decay-at-read + mastery bands, wired from a scored attempt.
- [ ] `attempt_events` append-only + replay tool rebuilds `attempt_task_state` (verify replay tool runs).

### 1.4 Static hint ladder + rules-only recommendation
- [ ] Static hint ladder: authored hints served, penalty tracking applied to score, no AI path invoked.
- [ ] Rules-only recommendation: candidate gen (curriculum-adjacent + remediation) → eligibility filter →
      simple scoring, returns reason codes, no ML/embedding call.

### 1.5 Lab lifecycle (start → active → idle → suspended → resume)

**Audit result:** the lifecycle is implemented. For T1 guided labs the doc explicitly says NOT to
snapshot/resume workspaces (`Server.Snapshot`/`Restore` return `Unimplemented` on purpose) — an abandoned
lab is re-provisioned from fixture. The real T1 lifecycle is:
`CREATED → PROVISIONING → READY → IN_PROGRESS → SUBMITTED`, plus `IN_PROGRESS → (idle 15min, two-signal) →
ENV_DESTROYED(idle) → SUSPENDED → CACHED`, and reaper/budget force-destroy → `ENV_DESTROYED → SUSPENDED`.
Gap is coverage + E2E proof, not the feature.

- [ ] Integration test drives the full T1 lifecycle end-to-end against a real orchestrator incl. the
      idle→`SUSPENDED`→`CACHED` path and "resume" = fresh re-provision from fixture.
- [ ] Reaper force-destroy of a stuck env publishes `ENV_DESTROYED`; practice-core `env-destroyed.consumer`
      transitions the attempt (guarded against regressing `TERMINAL`/`CACHED` — verify existing guard) and
      frees the learner's concurrency slot.
- [ ] Verify `handleEnvironmentDestroyed` idempotency under redelivery (NATS at-least-once).

---

## 2. Integration points (from PLAN.md Phase 1)

- [ ] **IP1 Provision contract:** practice-core Attempt Service → orchestrator `Provision` over the real gRPC
      contract (mTLS + shared-secret on, per `orchestrator/internal/orchestrator/auth.go`). Fake only behind
      an explicit flag.
- [ ] **IP2 Validator credentials:** `MintValidatorCredentials(env_id, ttl)` returns working read-only K8s
      creds; Validator Runner uses them; creds expire.
- [ ] **IP2b Command telemetry:** Session Broker emits `COMMAND_EXECUTED` on `env.telemetry.*`; practice-core
      `command-executed.consumer` persists to the event store. Verify subject naming matches both sides.
- [ ] **IP3 Cost meter → budget:** orchestrator `costmeter` emits `env.usage_meter` rows; practice-core
      eligibility/budget evaluator reads them and blocks new starts at 100%; orchestrator force-destroys at
      120%. Verify both halves.
- [ ] **IP4 Reaper ↔ attempt state:** covered in §1.5; verify no cross-boundary direct DB writes.
- [ ] Contract drift check: `contracts/orchestrator.proto`, `contracts/events.md`,
      `contracts/activity_spec.schema.json` regenerated stubs match both services' code.

---

## 3. Test coverage

### 3.1 Unit — critical backend components with ZERO current coverage
- [x] `orchestrator/internal/reaper/reaper_test.go` — **10 tests** (testcontainers Postgres via new
      `internal/testsupport`). Covers `Register` upsert idempotency, `Unregister` no-op on unknown, `sweep()`
      destroys only past-deadline rows + clears them, **failed destroy leaves row registered → retry succeeds
      next sweep**, no-op when nothing overdue, `OrphanSweep` destroys unrecorded namespaces + skips known +
      handles lister error, `envIDFromNamespace`. — verified: `go test ./internal/reaper/` PASS
- [x] `orchestrator/internal/idledetect/detector_test.go` — **9 tests**. Added a `readCPU cpuReader` seam on
      `Detector` (prod wiring installed by `New`). Covers **the two-signal guarantee** (never destroys on
      silence while CPU high), never destroys before silence window, destroys only after *both* windows,
      CPU-spike resets the low-CPU timer, metrics-read error never destroys, T-3min warning fires once,
      `RecordActivity`/`Untrack`, `cpuPercentFromMetrics` incl. divide-by-zero. — verified: PASS
- [x] `orchestrator/internal/warmpool/manager_test.go` — **11 tests** (miniredis). Added a `fillOneFn` seam
      on `Filler`. Covers `Claim` hit/miss/`redis.Nil`, **concurrent claims never hand out the same env
      (SPOP atomicity, 50 envs / 100 claimers)**, `Size`, `fillOnce` fills exactly the deficit, no fill
      at/above target, **stops hammering a failing blueprint mid-tick**, one failing blueprint doesn't block
      a healthy one, `Run` no-targets returns. — verified: PASS
- [ ] `orchestrator/cmd/orchestrator/main.go` — `parseWarmPoolTargets` still untested (pure function; low
      risk). Extract to a tested helper or add `main_test.go`.
- [x] `orchestrator/internal/metrics/metrics_test.go` — **2 tests**: `/metrics` renders all registered names
      the alert rules query; `attempt_cost_usd` histogram has a bucket boundary at exactly $0.08.

### 3.2 Unit — fill gaps in existing-but-thin coverage
- [ ] `costmeter`: confirm `decideBudgetAction` table tests cover the fast-jump 50→80 case and once-per-tier
      semantics (doc comment claims a test exists — verify `meter_test.go`).
- [ ] practice-core `attempt.service` state-machine: every illegal transition rejected; idempotency keys;
      eligibility + quota checks. Verify existing specs, add missing transitions (idle/suspend/resume).
- [ ] `web` workspace: `WorkspaceTerminal` reconnect/backoff logic unit-tested (currently no spec for it).

### 3.3 Integration
- [ ] **Orchestrator flow** integration test: `Provision → Connect → (exec) → Validate → Snapshot → Restore →
      Destroy` against a real single-node k3s (testcontainers or the compose `dev-a` profile). Gated behind a
      build tag / env so it doesn't run in unit CI.
- [ ] **Lab lifecycle** integration test (cross-service): practice-core + orchestrator + NATS + Postgres via
      compose; drive one attempt through the full state machine incl. idle→suspend→resume and reaper destroy.
- [ ] **IP round-trips**: one test each for IP1/IP2/IP2b/IP3 exercising the real contract.
- [ ] **Cost & performance tracking** integration test: run N attempts, assert `usage_meter` rows accrue,
      assert a per-attempt cost aggregate is queryable and within the `< $0.08` threshold on the T1 image.

### 3.4 Load
- [ ] Load harness committed under `evaluation/phase1/load/` (k6 or Locust): ramps to **200 concurrent
      learners** each doing ≥3 labs (provision → 20+ commands → validate → submit → destroy).
- [ ] Harness records: provision success rate, **time-to-ready p95**, validator ERROR rate, cost/attempt,
      WS reconnect success, reaper orphan count during + after the run.
- [ ] `namespace churn at 3× projected peak` soak (PLAN.md R9) — script + pass criteria (zero orphans).
- [B] **Execute** the 200-learner load run on a real cluster and commit results to
      `evaluation/phase1/results/loadtest-<date>.md` + raw output.

---

## 4. Observability (metrics, logs, alerts)

### 4.1 Metrics
- [x] Orchestrator exposes Prometheus `/metrics` + `/healthz` on `ORCHESTRATOR_METRICS_PORT` (default 9090),
      a separate server from the gRPC/WS data planes. — `orchestrator/internal/metrics`, mounted in
      `cmd/orchestrator/main.go`; verified: `metrics_test.go` PASS + `go build ./...`
- [x] Instrumented: `orchestrator_provision_duration_seconds{tier,source}` (histogram, buckets around 20s SLO),
      `orchestrator_provision_total{tier,result}`, `orchestrator_warm_pool_depth{blueprint}`,
      `orchestrator_warm_pool_claim_total{result}`, `orchestrator_reaper_destroyed_total{reason}`,
      `orchestrator_reaper_orphans_found_total`, `orchestrator_idle_destroyed_total`,
      `orchestrator_budget_action_total{tier}`, `orchestrator_usage_meter_cost_usd{attempt_id}` (live),
      `orchestrator_attempt_cost_usd` (histogram, **bucket boundary at $0.08**, observed once at teardown),
      `orchestrator_ws_sessions_active`, `orchestrator_ws_connections_total`.
- [x] practice-core exposes `GET /metrics` (`@prometheus-io/client`, `MetricsModule`): default Node metrics +
      `practice_core_attempt_transition_total{to}` (instrumented in `attempt.repository.transition()` — the
      single chokepoint), `practice_core_validator_result_total{validator_type,status}` (in
      `validator-runner.service`, gives the ERROR-rate exit criterion), scoring/recommendation duration
      histograms (defined; call-site wiring pending). — verified: `metrics.service.spec.ts` 4 tests PASS,
      165/165 jest.
- [~] `attempt_id` as a log field on every cross-service log (doc §13.5 #1). Orchestrator side now done for
      the lifecycle/teardown paths (§4.2 above — `attempt_id`/`env_id`/`reason` are structured fields).
      practice-core side (NestJS request logs carrying `attempt_id`) is still open.

### 4.2 Logs
- [x] Structured (JSON) logging in orchestrator. New `internal/logging` package: `slog` JSON handler
      installed as the process default by `logging.Init()` in `main.go`, plus a `log.SetOutput` bridge so
      not-yet-converted `log.Printf` still lands as JSON on the same stream (no format split). Canonical
      field-name constants (`env_id`, `attempt_id`, `reason`, `component`, `namespace`, `error`, `count`).
      Converted paths: `reaper` (all lines), `idledetect` (all lines), `costmeter` (usage-emit + full
      budget chain — 50/80/120), `orchestrator` lifecycle (cold-provision, warm-pool hit, env-row write,
      reaper-register, fixture-apply-skip) and `orchestrator` teardown (`destroyer.go`, all lines). RPC-
      result diagnostics (validator/shell/fault) left on the std bridge — outside §4.2's lifecycle scope.
      — verified: `go build`/`vet`/`gofmt`/`test ./...` clean; smoke run shows e.g.
      `{"level":"INFO","msg":"cold-provisioning environment","component":"orchestrator","env_id":"…","attempt_id":"…","source":"cold"}`.
- [ ] Session recording (`S3RecordingSink`) confirmed writing asciicast to MinIO/S3 for a real session.

### 4.3 Alerts
- [x] Prometheus alert rules committed: `evaluation/phase1/observability/alerts.yml` —
      `TimeToReadyP95High` (>20s/10m), `ProvisionSuccessLow` (<99%), `ValidatorErrorRateHigh` (>0.5%),
      `CostPerAttemptHigh` (p90 >$0.08), `OrphanEnvironmentsDetected` (>0/1h), plus operational:
      `BudgetHardStopFiring`, `WarmPoolChronicMiss`, `OrchestratorMetricsEndpointDown`. Thresholds = doc's
      numbers. — verified: YAML parses.
- [x] Grafana dashboard JSON: `evaluation/phase1/observability/dashboard.json` — one panel per exit criterion
      + throughput/warm-pool/teardown/transition panels. Plus `prometheus.yml` scrape config and a README
      with the exact PromQL for each criterion. — verified: JSON parses.
- [B] Wire the rules into a running Prometheus/Alertmanager + Grafana against a production-like cluster and
      confirm they evaluate.

---

## 5. Real authentication (no bypass in critical paths)

**Audit result:** practice-core already has a real `AuthGuard` that verifies a signed JWT and validates the
`role` claim (`auth.guard.ts`), plus `attempt-ownership.guard` for per-attempt authz. The bypass is that the
**only token issuer** is `POST /v1/auth/dev-login` — no password, fixed `DEMO_USER_ID`/`DEMO_TENANT_ID`.

Decision (2026-08-27): keep `dev-login` behind an **off-by-default** env flag; build real auth as a clean
`AuthProvider` seam with **LTI 1.3** as the first implementation (not wired to a live LMS yet).

- [ ] Introduce `AuthProvider` interface in practice-core (`verify(rawToken) → AuthClaims`). Current
      `AuthGuard` calls it instead of `JwtService.verify` directly.
- [ ] `DevLoginAuthProvider` (existing shared-secret behaviour) — only registered when
      `AUTH_DEV_LOGIN_ENABLED=true`. Default: **disabled**; `AuthController.devLogin` returns 404 when
      disabled.
- [ ] `Lti13AuthProvider` — verifies the LMS launch JWT against a configured JWKS URL, checks
      `iss`/`aud`/`nonce`/`exp`, maps `sub` + `https://purl.imsglobal.org/spec/lti/claim/context` → learner
      + tenant, issues our own short-lived session JWT. Config via `LTI_ISSUER`, `LTI_JWKS_URL`,
      `LTI_CLIENT_ID`. Unit-tested with a local JWKS fixture; **not** required to be wired to a real LMS for
      Phase-1 code completion.
- [ ] `web`: `auth-token.ts` only calls `dev-login` when a build-time flag says dev mode; otherwise expects a
      session token from the LTI launch handler / redirect. Add a `/launch` route that accepts the LTI
      `id_token` POST and establishes the session.
- [x] `demo-context.ts` `DEMO_USER_ID`/`DEMO_TENANT_ID` no longer referenced anywhere (grep clean across
      `web/src/`, only the doc comment in `session.ts` mentions the old names). Frontend identity now comes
      from the session JWT via `useSession()` — see §1.1 above. This is the frontend half of the seam; the
      token issuer is still `dev-login`, so `DevLoginAuthProvider` / `Lti13AuthProvider` (above) remain the
      backend half.
- [~] Orchestrator RPC auth: `ORCHESTRATOR_SHARED_SECRET` is now set by the compose `app` profile for all
      three services (matching values), and the smoke test confirms auth is actually enforced end-to-end.
      `ORCHESTRATOR_TLS_ENABLED` is still off in compose (documented as a Phase-2 closure item); the
      "documented as **required** for prod" wording is still to be added to a deploy doc.
- [ ] Negative tests: request with no token → 401; expired token → 401; wrong-tenant attempt access → 403
      (verify `attempt-ownership.guard`).

---

## 6. Error handling & retry (no mocks/stubs in critical paths)

- [ ] `GrpcOrchestratorClient`: deadline per RPC, retry with backoff on `UNAVAILABLE`/`DEADLINE_EXCEEDED` for
      idempotent calls (`Connect`, `Validate`, `Destroy`), no retry on `Provision` without an idempotency key.
- [ ] Provision failure path: practice-core transitions attempt to `PROVISION_FAILED`, surfaces a real error
      to the UI, and the reaper still owns cleanup of any partial env.
- [ ] NATS consumers (`command-executed`, `env-destroyed`): ack only after commit, redelivery-safe
      (idempotent), dead-letter on repeated failure.
- [ ] WS Gateway: backpressure + rate limit enforced; oversized frames rejected; auth failure closes cleanly.
- [ ] Reaper/idle/budget destroy failures are retried on the next tick (verify — reaper already does; confirm
      idle + budget paths don't drop the env).
- [ ] Grep sweep: no `fake-`, `stub`, `TODO`, `FIXME`, `mock` on any production code path in
      `attempt/`, `wsgateway/`, `sessionbroker/`, `reaper/`, `warmpool/`, `costmeter/`, `idledetect/`.

---

## 7. Exit criteria validation (doc §13.1) — MEASURED, not asserted

Each row needs a committed artifact in `evaluation/phase1/results/`. All are `[B]` until run on a
production-like cluster.

- [B] **≥ 200 learners** complete ≥ 3 labs each (load run) — `loadtest-<date>.md` with concurrency graph.
- [B] **Provision success ≥ 99%** — from `provision_total{result=}` over the run.
- [B] **Time-to-ready p95 ≤ 20s** — from `provision_duration_seconds` histogram over the run.
- [B] **Validator ERROR rate < 0.5%** — from practice-core validator metrics over the run.
- [B] **Cost/attempt < $0.08** — from `usage_meter` per-attempt aggregate over the run.
- [B] **Measured Elo available per lab** — export query + sample output committed.
- [B] **Zero orphan environments** sustained during + 1h after the run — `reaper_orphans_found_total` == 0.
- [ ] `evaluation/phase1/README.md` documents exactly how to reproduce every measurement (commands, dashboards,
      queries) so the operator can run it unattended.

**Progress toward §7 in this session:** the compose `app` profile now makes the whole system bring up with
one command (`docker compose --profile app up`), and `evaluation/phase1/smoke/` proves the attempt →
provision → real-k3s-pod → terminal path end-to-end. That is the *functional* prerequisite for the load
run. It does NOT satisfy any §7 row: a single-node Docker-Desktop k3s cannot sustain 200 concurrent
workspace pods, and none of the seven measured artifacts exist yet. The load harness
(`evaluation/phase1/load/`) is still unwritten — building it is the next concrete step; executing it
remains `[B]` on a real multi-node cluster.

---

## 8. Closure cross-check (mandatory final step)

Phase 1 is **CLOSED** only when ALL of the following hold, verified in one final pass:

1. [ ] Every `[ ]`/`[~]` item above is `[x]` with a verification note, OR is `[B]` and its results artifact is
       committed and passes threshold.
2. [ ] `go build ./... && go vet ./... && go test ./...` clean in `orchestrator/` (incl. the new
       reaper/idledetect/warmpool tests).
3. [ ] `npm test` clean in `practice-core/` and `web/`.
4. [ ] Integration suite (compose-backed) green.
5. [ ] All seven §7 exit-criteria artifacts committed to `evaluation/phase1/results/` and each meets its
       threshold.
6. [ ] No stub/fake/mock on any critical path (§6 grep sweep clean).
7. [ ] Real auth path verified; `dev-login` disabled by default.
8. [ ] A reviewer other than the implementer signs off against this list.
9. [ ] This file's status line changed to `CLOSED — <date>` and the sign-off recorded below.

**Sign-off:** _(name, date)_ — pending.

### Progress log

**2026-08-27 (completable-subset pass):** Delivered and verified —
- §1.1 · three service `Dockerfile`s + compose `app` profile + `db-migrate-orchestrator` /
  `kubeconfig-internal` one-shots + `k3s --tls-san=k3s`.
- §1.1 · `evaluation/phase1/smoke/` (script + smoke compose override + README) — E2E: compose up →
  attempt → real-k3s workspace pod → terminal WS URL → destroy. Result artifact committed.
- §1.1 / §5 · frontend identity from the session JWT (`web/src/lib/session.ts`, 7 call sites);
  `DEMO_USER_ID`/`DEMO_TENANT_ID` removed.
- §4.2 · `orchestrator/internal/logging` (slog JSON) + conversion of reaper / idledetect / costmeter /
  orchestrator-lifecycle / destroyer paths.

Deferred (need a cluster + content team, per owner's direction):
- §1.2 · ≈60 reference-solution artifacts. §1.3 / §3.3 · content-CI-green (gated on the above).
- §7 · the 200-learner load run + all 7 measured exit-criteria artifacts; the load harness itself
  (`evaluation/phase1/load/`) is not yet written.
- §5 backend · `AuthProvider` seam, `DevLoginAuthProvider` (off-by-default), `Lti13AuthProvider`, `/launch`.
- §4.2 · practice-core structured request logging; `S3RecordingSink` real-session verification.

---

## 9. Execution order (recommended)

1. §1.1 workspace↔orchestrator default path + §2 IP1 (unblocks everything downstream).
2. §3.1 the three zero-coverage unit test files (fast, high value, no infra).
3. §4.1 metrics endpoints + instrumentation (needed to *measure* anything).
4. §5 auth seam + LTI provider + dev-login flag.
5. §6 error handling/retry hardening.
6. §3.3 integration suite (compose-backed).
7. §1.2–1.5, §3.2 feature/coverage gap-fill.
8. §3.4 + §4.3 load harness + alert rules (deliver tooling).
9. **[operator]** §7 run the measurements on a real cluster, commit artifacts.
10. §8 closure cross-check + sign-off.
