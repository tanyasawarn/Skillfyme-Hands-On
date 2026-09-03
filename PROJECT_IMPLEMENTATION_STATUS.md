# Practice Engine — Implementation Status (as of 2026-09-03)

A detailed record of everything built so far in this repository, mapped against the
architecture blueprint (`memory.md`) and the phased delivery plan (`PLAN.md`).

> **Scope of this document:** what *exists in the tree today* — code, tests, contracts,
> content, infra-as-code, and the verification that has actually been run. Items that are
> planned-but-not-built, or built-but-blocked on real infrastructure (multi-node cluster,
> AWS Organizations account, `ANTHROPIC_API_KEY` + SME time), are called out explicitly.

---

## 1. What the product is

Three products in one platform (`memory.md` Part 0):

1. **A workload execution platform** — untrusted learners run arbitrary Linux/Docker/K8s/
   Terraform/cloud commands in disposable sandboxes.
2. **A measurement platform** — turning "did stuff in a sandbox" into a defensible,
   reproducible, event-sourced skill claim.
3. **A curriculum-integrated learning product** — skill graph, BKT mastery, adaptive
   recommendation, progression.

Core pipeline: **evidence → validators → signals → scoring → BKT mastery → recommendation.**
Everything else (terminal, editor, cloud accounts, AI mentor) is plumbing feeding that pipeline.

**Learning progression / modes** (one Activity Runtime with five knobs, not three engines):
`Learn → Follow → Implement → Troubleshoot → Design → Build → Defend`, realised as three
activity modes:

| Mode | Difficulty | Status in repo |
|---|---|---|
| `GUIDED_LAB` | L1–L3 | **Built & exercised end-to-end** (Phase 1) |
| `PRODUCTION_SIM` | L3–L4 | **Built** — fault injection, incident-note AI rubric, process-telemetry scoring (Phase 2) |
| `PROJECT` | L4–L5 | **Built, code-complete, gated** — milestone state machine, T3 driver, viva; not run on real cloud (Phase 3) |

---

## 2. Repository layout

Mirrors the D6 service boundaries:

| Path | What it is | Language / stack |
|---|---|---|
| `/orchestrator` | Environment Orchestrator — real separate service | Go 1.26, gRPC, client-go |
| `/practice-core` | Practice Core modular monolith (catalog, attempt, skill, scoring, recommendation, evaluation, project, analytics, privacy) | NestJS 11, Kysely, Postgres |
| `/web` | Unified Activity Runtime UI | Next.js 16, React 19, xterm.js, Monaco |
| `/contracts` | Frozen cross-team contracts | protobuf + buf, JSON Schema, event taxonomy |
| `/content` | Content-as-code: activities, faults, rubrics, reference solutions | YAML |
| `/evaluation` | Phase-1 measurement harnesses + `/evaluation` service is a *bounded module inside practice-core* (not extracted) | shell, k6, Python |
| `/infra` | Terraform / OpenTofu + Helm + compose for AWS org, account baseline, Forgejo, ClickHouse, nuke, budget, cost | HCL, Helm, compose |
| `/ai-gateway` | **Not built** — Phase 4 scope (README only) | — |
| `/scripts` | CI helpers, cert generation, per-track check scripts | shell |

---

## 3. Contracts (`/contracts`) — frozen, governed

- **`orchestrator.proto`** — `EnvironmentOrchestrator` gRPC service. RPCs implemented:
  `Provision`, `Connect`, `Destroy`, `MintValidatorCredentials`, `InjectFault`,
  `CaptureBaseline`, `CheckRegression`, `ExecValidator`, `ExecShell`.
  `Snapshot` / `Restore` are defined (with the T3 `SnapshotManifest` message) but still
  return `Unimplemented` on purpose — for T1 guided labs the blueprint says not to
  snapshot; T3 wiring is gated (see §8).
- **`events.md` + `contracts/events/*.schema.json`** — full attempt-event taxonomy:
  lifecycle, execution (`COMMAND_EXECUTED`, `FILE_CHANGED`, …), validation, assistance,
  environment (`ENV_DESTROYED`, `FAULT_INJECTED`, `IDLE_DETECTED`, `TTL_WARNING`),
  scenario (`TICKET_OPENED`, `ESCALATION_FIRED`), project (`MILESTONE_SUBMITTED`,
  `MILESTONE_GATED`, `DEFENCE_MESSAGE`), cloud-account (`ACCOUNT_CLAIMED`, `ACCOUNT_NUKED`,
  `ACCOUNT_QUARANTINED`). Payload JSON Schemas exist for the newer types.
- **`activity_spec.schema.json`** — the authored-content contract. Modes
  `GUIDED_LAB | PRODUCTION_SIM | PROJECT`; difficulty `L1–L5`; tiers
  `BROWSER | SHARED_CONTAINER | ISOLATED_VM | CLOUD_ACCOUNT`; `milestones[]`, `defence`,
  `environment.cloud` (regions + SKU exceptions), and per-type `validators[].config` for
  the 6 T3 validator types.
- **`fault.schema.json`** — fault-primitive contract (35 authored faults validate against it).
- **Governance**: `buf.yaml` (`buf lint` STANDARD + `buf breaking` FILE), `buf.gen.yaml`
  (version-pinned remote plugins, deterministic codegen), `CHANGELOG.md` (every post-freeze
  change classified PATCH/MINOR/MAJOR), `README.md` (additive-only-between-majors rule),
  `.github/CODEOWNERS` requiring both track owners on any `contracts/` PR, and a `contracts`
  CI job (`buf lint` + `buf breaking` vs `origin/main` + stub-freshness `git diff --exit-code`).

---

## 4. Orchestrator (`/orchestrator`) — Go, Dev A track

**Build state:** `go build ./... && go vet ./... && gofmt -l . && go test ./...` clean —
~28 packages, all green (per Phase 3 closeout).

### Go packages and what each does

| Package | Responsibility | Phase |
|---|---|---|
| `cmd/orchestrator` | Process entrypoint, wiring, `config.Load()`, metrics server, NATS sinks, `cloudlifecycle.go`, HTTP hooks (`/cloud/budget-breach`) | 1–3 |
| `internal/orchestrator` | gRPC server (`server.go`), `AuthInterceptor` (shared bearer, constant-time), mTLS (`RequireAndVerifyClientCert`), per-RPC `attempt_id` ownership checks (`checkEnvironmentOwnership`), `resolveTier()` | 1–2 |
| `internal/k8s` | T1/T2 provisioning: namespace-per-env, ResourceQuota, LimitRange, NetworkPolicy default-deny + egress-proxy allow, PodSecurity restricted, RBAC, discovery ClusterRoleBinding, API-server egress, kubeconfig minting, `WaitForPodReady`, `Destroy` | 1–2 |
| `internal/warmpool` | Redis SPOP CAS warm-pool claim + background filler (11 tests, concurrency-safe) | 1 |
| `internal/fixture` | Idempotent, ordered, checksummed fixture apply (`env.fixture_applied`, migration 0003). Real fixtures: `fx.k3s-ready.v1`, `fx.pod-crashloop.v1`, `fx.node-app-repo.v1`, 4 `fx.git-repo-*` | 1 |
| `internal/validation` | Typed validator execution: `SHELL_ASSERT`, `SHELL_JSON`, `FILE_EXISTS/CONTENT`, `FILE_PARSE`, `K8S_ASSERT` (incl. `-o jsonpath=` + `kubectl exec <pod>`), `NO_REGRESSION`, `HTTP_SLO`; `health_gate.go` (`HTTP_PROBE`) | 1–2 |
| `internal/faultinjection` | Fault-manifest execution after health gate. **12 / 35 handlers wired** with real K8s mutations; 23 correctly deferred with typed `ErrUnsupportedMechanism`. Injection-safe param handling (`%q`, typed parse, argv-not-interpolation) — two real vulns (JSON-structure injection, shell injection) found & fixed with regression tests | 2 |
| `internal/sessionbroker` | PTY proxy + telemetry tap: `COMMAND_EXECUTED` capture via `PROMPT_COMMAND` shell hook (real cmd text + exit code + hash), `blast_radius` forbidden-command detection point | 1–2 |
| `internal/wsgateway` | Stateless WS gateway, per-socket JWT/session auth, attempt-scoped authz, terminal WS URL minting | 1 |
| `internal/reaper` | `environment_reaper` table + force-destroy job + `OrphanSweep` (10 tests, retry-on-failed-destroy) | 1 |
| `internal/idledetect` | Two-signal idle detection (silence + low CPU), T-3min TTL warning (9 tests, two-signal guarantee) | 1 |
| `internal/ttl` | TTL clocks / long-running-op suppression | 1 |
| `internal/costmeter` | 60s `usage_meter` emission + budget evaluator chain (50/80/100/120%), `budgetDestroyFn` → real `Destroyer` (15 tests; `decideBudgetAction` pure fn; a real once-per-tier bug found & fixed) | 1 |
| `internal/metrics` | Prometheus `/metrics` + `/healthz` on a separate port; histograms with a bucket boundary at exactly $0.08 | 1 |
| `internal/logging` | `slog` JSON handler as process default + `log.SetOutput` bridge; canonical field constants; reaper/idledetect/costmeter/lifecycle/teardown paths converted | 1 |
| `internal/audit` | Durable Postgres `env.audit_log` (migration 0004), closed Action/Outcome sets, wired into Provision/Destroy/InjectFault/MintValidatorCredentials/ExecShell via defer | 1 |
| `internal/regression` | `CaptureBaseline` / `CheckRegression` (regression-baseline table, migration 0002) | 2 |
| `internal/telemetry` | NATS sink for telemetry events (`env.telemetry.*`) | 1–2 |
| `internal/accountpool` | **Phase 3.** AWS Account Pool Manager: `AVAILABLE → IN_USE → NUKING → (AVAILABLE | QUARANTINED)`, `env.cloud_account` (migration 0005) + Redis SPop CAS; claim runs budget + baseline + SKU-tag + `ACCOUNT_CLAIMED`; partial-claim rollback; `Filler` re-drives stuck-NUKING (8 tests) |
| `internal/credbroker` | **Phase 3.** OIDC + `AssumeRoleWithWebIdentity` STS broker: blocking initial mint + refresh loop at 50% of TTL, `Registry` per active T3 attempt, `StopFor(attemptID)` (6 tests) |
| `internal/cloudnuke` | **Phase 3.** `Sweeper` — nightly re-nuke of AVAILABLE + QUARANTINED accounts; non-empty AVAILABLE ⇒ QUARANTINE + page (4 tests) |
| `internal/cloudbudget` | **Phase 3.** `Enforcer.HandleBreach` (<100% warn; ≥100% revoke STS + terminate T3, idempotent) + `LaunchCap.Allow` (`ResourceExhausted` at cap) (5 tests) |
| `internal/cloudcost` | **Phase 3.** Independent hourly Cost Explorer poll per IN_USE account grouped by `attempt_id` tag → `usage_meter.cloud_cost_usd`; daily CUR reconciliation overwrites the estimate (4 tests) |
| `internal/cloudaws` | **Phase 3.** `RealClient` (aws-sdk-go-v2 STS + shell-out to `aws`/`aws-nuke`/`tofu`) and `FakeClient` for tests |
| `internal/t3driver` | **Phase 3.** `Driver.Provision` = accountpool.Claim → credbroker.Add → PodManager.StartWorkspacePod (editor + broker sidecar, no gVisor); `Connect` = editor + terminal WS URLs w/ scoped session token; `Destroy` = broker.StopFor → accountpool.Release → delete pod (4 tests) |
| `internal/snapshotstate` | **Phase 3.** `Snapshot` = `terraform state pull` + filtered `kubectl get all -A` + `aws resource-explorer-2 search` → 3 S3 blobs (manifest mirrors `pb.SnapshotManifest`); `Restore` = read manifest → reclaim original account → re-provision → `terraform init -reconfigure` + `apply` (5 tests) |
| `internal/clusterbootstrap` | Installer scaffolding for in-cluster tooling fixtures (prometheus/istio/argocd/…) — partial |
| `internal/manifests`, `internal/loop`, `internal/envstatus`, `internal/destroyreason`, `internal/snapshotstate`, `internal/testsupport` | Support / shared helpers, testcontainers Postgres | 1–3 |

### Kubernetes manifests

- `manifests/t1/` — `runtimeclass-gvisor.yaml`, `orchestrator-netpol.yaml` (gRPC ingress
  restricted to practice-core pod-selector), `egress-proxy.yaml` (Squid default-deny +
  allowlist), `daemonset-image-prepull.yaml` (pre-pulls the one real image that exists),
  `node-pool-taint.md`.
- `manifests/t2/` — `runtimeclass-kata.yaml`, `node-pool-taint.md` (threat-model note for
  `privileged: true` + Kata as the compensating control).
- `manifests/platform/` — namespace, orchestrator RBAC, mTLS certs, orchestrator +
  practice-core Deployments, NetworkPolicies, kustomization, secret templates.
- `images/linux-tools/Dockerfile` — the T1 workspace image (bash + `kubectl`/`git`/`jq`/
  `curl`/`python3`); rebuilt & pushed this session.

### DB migrations (`env` / `billing` schemas — Dev A)

`0001_env_and_billing` (incl. `usage_meter.cloud_cost_usd`), `0002_regression_baseline`,
`0003_fixture_applied`, `0004_audit_log`, `0005_cloud_account`.

### Docs

`orchestrator/docs/` — `t2-setup-and-operations.md`, `t2-cost-optimization{,-100}.md`,
`quarantine-runbook.md`.

---

## 5. Practice Core (`/practice-core`) — NestJS, Dev B track

**Build state:** `tsc --noEmit` clean; `npm run lint:boundaries` clean;
**unit 210/210**; **integration 132 passed / 0 failed / 7 skipped** (against the live stack,
2026-09-03). One historically-flagged `attempt-lifecycle` cooldown integration failure
predates current work and is tracked in `PHASE1_REMEDIATION.md`.

### Modules

| Module | Responsibility |
|---|---|
| `catalog` | Content-as-code: `ActivitySpecReader`, `SpecLintService` (Ajv against `activity_spec.schema.json`), `CatalogRepository`, publish pipeline. `mode: PROJECT` support (milestones/defence/cloud/validator-config). |
| `curriculum` | Course→Module→Topic→Subtopic; `topic_skill` mapping; DevOps + SRE tracks seeded (GenAI track seeded but activity content largely unauthored). |
| `skill` | Skill DAG (edge types REQUIRES/BUILDS_ON/SIBLING/SPECIALIZES/SUPERSEDES), materialised `skill_closure` via recursive CTE rebuilt on publish. ~76 skills live (DevOps track). |
| `attempt` | Both state machines (content + attempt lifecycle), eligibility + quota + cost-budget checks, idempotency keys, 1-active-env-per-learner quota, `GrpcOrchestratorClient` (real by default; fake behind `USE_FAKE_ORCHESTRATOR`). |
| `event-store` | `attempt_events` append-only, monotonic seq; `ReplayService` rebuilds `attempt_task_state` from events; NATS consumers `command-executed`, `env-destroyed` (idempotent, at-least-once safe). |
| `evaluation` | **Bounded module, not extracted** (ESLint boundary rule enforces the seam). Validator Runner + typed catalogue, scoring engine (signals→criteria→profile), BKT mastery engine (4-param, difficulty-adjusted, decay-at-read, mastery bands), Elo calibration, retry/cooldown. Profiles: `sp.guided-lab.default`, `sp.production-sim.default`, `sp.project.default`. AI grading via `ClaudeAiGrader` (delimits + flags prompt injection) with `FakeAiGrader` for tests; incident-note rubric `rub.incident-note.v2` at 100% human review. |
| `evaluation/t3` | **Phase 3.** `T3ValidatorExecutor` routes `IAC_STATE`/`CLOUD_ASSERT`/`STATIC_ANALYSIS`/`TEST_SUITE`/`PERF_BENCH`/`CHAOS_PROBE` to typed handlers, delegates everything else to `GrpcValidatorExecutor`. `ShellRunner` backends: `OrchestratorShellRunner` (`ExecShell` RPC) / `LocalShellRunner`. Every platform failure → `ERROR`, never `FAIL`. Tested against real `terraform` + `tfsec` + committed fixture repos. |
| `evaluation/calibration` | `weighted-kappa.ts` (weighted Cohen's κ) + rubric-calibration harness. |
| `recommendation` | Rules-only four-stage pipeline (candidate gen: curriculum-adjacent + remediation; eligibility filter; weighted scoring; re-rank/package with structured `reason_code`). No ML / embeddings. |
| `project` | **Phase 3.** `ProjectService` state machine (`design→infra→implementation→hardening→final`, `LOCKED/OPEN/SUBMITTED/GATED_PASS/GATED_FAIL`), lazy-seed, per-milestone validator set + rubric slice, opens next LOCKED milestone on blocking `GATED_PASS`, emits `MILESTONE_SUBMITTED`/`MILESTONE_GATED`. `GitService` + `ForgejoClient` (per-learner repo provisioning, requirements-pack seeding, scoped push token, real `git push` over HTTP verified). `DefenceService` + `viva-model.port.ts` (`RealVivaModel` = direct forced-tool-use Anthropic call; `FakeVivaModel` = grounded from design-doc headers + real commit shas) → `DEFENCE_MESSAGE` turns, scored against `rub.reasoning.v1`. `ProjectScoringService.rollup` — milestone-weighted with a structural 40% cap on AI-derived components. `FakeProjectOrchestrator` default; `GrpcProjectOrchestrator` behind `PROJECT_ORCHESTRATOR_GRPC=on`. |
| `analytics` | **Phase 3.** `ClickHouseClient` (HTTP) + `AnalyticsIngestionService` (poll-tail of `attempt_events` by id with a durable `analytics.ingestion_cursor`, idempotent). `AnalyticsQueryService.eventRollup` serves from ClickHouse when `CLICKHOUSE_URL` set, Postgres otherwise. `GET /v1/admin/analytics/rollup`. Verified against real ClickHouse (compose profile). |
| `admin` | Minimal CMS (read/preview/publish-request), `cost-dashboard.service.ts` (**Phase 3**: `GET /v1/admin/cost/by-grain`, `/cost/accounts`, `/cost/budget-breaches`; degrades to `[]` when `env` schema absent), basic analytics. |
| `dashboard` | Home decision surface, mastery snapshot, continue/recommended/fix-a-weak-skill. |
| `privacy` | **Phase 3.** GDPR erasure (migration `0011_gdpr_erasure.sql`), `privacy.integration.spec.ts`. |
| `auth` | Real `AuthGuard` (signed JWT + role claim), `AttemptOwnershipGuard`. Token issuer is still `POST /v1/auth/dev-login` (rate-limited 5/60s). **`AuthProvider` seam + `Lti13AuthProvider` + `/launch` = not yet built** (Phase 1 §5 backend, open). |
| `common` | `BaseGrpcClient` (channel lifecycle), `AllExceptionsFilter` (no DB-detail leakage), helmet, `@nestjs/throttler` global (100/60s). |
| `metrics` | `GET /metrics` — `practice_core_attempt_transition_total{to}`, `practice_core_validator_result_total{validator_type,status}` (the ERROR-rate exit criterion), scoring/recommendation duration histograms. |
| `content-ci` | In-process pieces backing the content-CI runner. |

### DB migrations (`content`/`learner`/`attempt`/`skill`/`admin` schemas — Dev B)

`0000_schemas` … `0009_snapshot_stub`, `0010_project_mode` (`attempt.project_milestone_state`
+ `attempt.project_submission` + `ProjectRepository`), `0011_gdpr_erasure`.

### Tooling / scripts

`practice-cli` unified binary (`validate` / `test` / `publish`) wrapping `lint-content.ts`,
`content-ci.ts`, `publish-all-content.ts`; `seed-skills-{sre,genai}.ts`,
`seed-curriculum-*.ts`, `publish-all-content.ts`, `mint-dev-token.ts`,
`rubric-calibrate.ts`, `backfill-skill-order.ts`.

---

## 6. Web (`/web`) — Next.js 16 / React 19

**Build state:** `next build` clean; `vitest run` green (95/95 at last full count; component
library specs added since).

### Routes

`/` (Home), `/catalog` + `/catalog/[activityVersionId]`, `/skills`, `/history`,
`/attempts/[id]` (the workspace).

### Components / libs

- **Workspace**: `WorkspaceTerminal` (xterm.js + WebGL, reconnect banner, degraded/offline
  mode), `WorkspaceEditor` (Monaco, file API), task checklist / TaskRail, instructions pane.
- `components/sim/` — simulation UI surface (ticket, escalation); `components/attempt/` —
  attempt chrome.
- **Shared UI library** (`components/ui/`): `Button`, `Alert`, `Loader`, `Badge`/`StatusPill`
  (unified 3 parallel implementations; fixed a real per-page status-color disagreement),
  `PageContainer`, `EmptyState`, `SectionLabel`, `QueryBoundary`.
- `lib/api-client.ts` — single typed `request<T>()` wrapper, zero raw `fetch` bypasses.
- `lib/session.ts` — `useSession()` decodes `userId`/`tenantId`/`role` from the bearer JWT
  (`DEMO_USER_ID`/`DEMO_TENANT_ID` removed from every page).
- `lib/auth-token.ts` — token caching + proactive refresh (still bootstraps via `dev-login`).
- `lib/error-message.ts` — `toUserFacingError()`; `lib/attempt-status.ts` — canonical
  `ATTEMPT_STATUS_META`; `lib/sim.ts` — sim helpers.
- **Security**: Next.js security headers (nosniff, frame-options, referrer-policy,
  permissions-policy; deliberately not a full CSP yet because of Monaco/xterm workers);
  `dompurify` pinned `^3.4.14` via `overrides` (fixed 4 CVEs via the transitive
  `monaco-editor` dep); `npm audit` clean.

---

## 7. Content (`/content`)

| Kind | Count | Notes |
|---|---|---|
| Activity specs | **74** (`content/activities/*.yaml`) | 63 `lab.*` (all `GUIDED_LAB`, `SHARED_CONTAINER`), 11 `sim.*` (`PRODUCTION_SIM`). No `PROJECT`-mode content authored yet. |
| Fault primitives | **35** (`content/faults/*.yaml`) | All validate against `fault.schema.json`. 12 have wired orchestrator handlers. |
| Rubrics | 3 | `rub.incident-note.v2` (Phase 2, calibrated-ish, 100% human review), `rub.architecture.v3` (Phase 3 design gate, provisional), `rub.reasoning.v1` (Phase 3 viva, provisional). |
| Reference-solution dirs | 63 present (`content/activities/solutions/`) | ~35 have a *verified-working* `solution_apply` that actually makes validators pass against a real env; the rest declare the path but the scripts are still to be authored. |

### Content-CI verified state (local, k3s **runc not gVisor**, 2026-09-03)

- **25 labs STRICT PASS** (golden-path + flake×5 + strict null-path + timing + cost).
- ~10 more pass golden + flake with one weak null-path validator (solution correct;
  one-line spec tightening pending).
- **~35 labs with a verified-working reference solution** — meets PLAN.md's "25–35 guided
  labs L1–L3" Phase-1 target.
- **24 BLOCKED**, all documented: 7 need a fuller workspace image (docker/DinD, helm,
  terraform), 12 need in-cluster installer fixtures (prometheus/istio/argocd/jaeger/elk/
  tekton), 1 needs an RBAC decision (HPA API group), ~2 misc (script timeout). **None
  require gVisor.**
- Skill graph: 76 skills + `skill_closure` rebuilt. Curriculum: DevOps + SRE seeded.
- 74/74 activities published through the real `SpecLintService → CatalogRepository` pipeline.

---

## 8. Phase-by-phase status

> Two independent "phase" numbering schemes exist in the repo, both tracked:
> **(A)** the *product roadmap* (`PLAN.md` Phases 1–5: MVP → Sims+T2 → Projects+T3 → AI
> Mentor → Scale), and **(B)** a *centralization/refactor plan* (`PLAN_PHASE4.md` /
> `PLAN_PHASE5.md`, Phases 0–5, extract-a-component work). Below is the product roadmap.

### Phase 0 — Contracts & Scaffolding — ✅ COMPLETE
Proto, event taxonomy, activity-spec schema, Postgres schema-per-bounded-context,
docker-compose (Postgres/Redis/NATS/k3s/registry/MinIO), CI skeleton (+ a `contracts` job
and a self-hosted `content-ci` job). Exit criterion (stub gRPC call end-to-end) met.

### Phase 1 — MVP: Guided Labs, one track — ⚠️ FUNCTIONALLY COMPLETE, NOT FORMALLY CLOSED

**Built & verified LIVE (local, real services — real orchestrator from source, real
practice-core, real k3s/NATS/Postgres/Redis/MinIO):**

- All 5 integration points (IP1 real Provision; IP2 `MintValidatorCredentials` read-only
  token; IP2b `COMMAND_EXECUTED` → NATS → event store; IP3 `usage_meter` → budget evaluator;
  IP4 reaper/Destroy → `ENV_DESTROYED` → attempt state).
- T1 isolation primitives on k3s: namespace-per-env, ResourceQuota, LimitRange,
  NetworkPolicy default-deny + egress-proxy, PodSecurity `restricted` (verified rejecting a
  privileged pod).
- Full E2E learner pipeline: attempt → provision (1.7s warm) → start → execute → submit →
  4× `VALIDATOR_RESULT` → 2× `TASK_PASSED` → `EVALUATED` → `attempt_score` final 1.0000 →
  BKT `p_mastery` 0.988 band "Mastered" → Elo moved 1200 → 1198. Event store append-only
  seq 1–13.
- Lab lifecycle incl. idle→`SUSPENDED`→`CACHED` and reaper force-destroy → `ENV_DESTROYED`.
- Orchestrator: Prometheus `/metrics` + alert rules (`evaluation/phase1/observability/`) +
  Grafana dashboard JSON; structured JSON logging.
- Auth: real `AuthGuard` + `AttemptOwnershipGuard` + rate limiting; frontend identity from
  session JWT.
- Load + soak *harnesses* committed (`evaluation/phase1/load/` k6 `load.js` +
  stdlib `load_driver.py` + `check-orphans.sh`; `evaluation/phase1/soak/`
  `namespace-churn-soak.sh`) — self-grading against the §13.1 thresholds.

**NOT done / blocked:**

- **gVisor** — no gVisor node available locally (`ORCHESTRATOR_GVISOR_ENABLED=false`); code
  path is wired and opt-in. Needs GKE-Sandbox or equivalent.
- **The 6–7 numeric exit criteria** (`memory.md` §13.1): ≥200 learners × ≥3 labs, provision
  ≥99% at scale, time-to-ready p95 ≤20s under load, validator ERROR <0.5% over a real
  corpus, cost/attempt <$0.08 from real billing, measured-Elo per lab at cohort volume,
  zero orphans sustained during + 1h after. All `[B]` — need a real multi-node cluster + a
  load-generation budget. Harness + measurement tooling are in place.
- **Auth backend seam**: `AuthProvider` interface, `DevLoginAuthProvider` (off-by-default),
  `Lti13AuthProvider`, `/launch` route — open.
- ~28 reference-solution script sets still to author; 24 labs BLOCKED on image/fixtures (see §7).
- Frontend, admin CMS, admin analytics, egress proxy + prepull DaemonSet — built/authored,
  not exercised E2E / not deployed to local k3s.

### Phase 2 — Production Simulations + T2 — ✅ COMPLETE (T2 gated off by design)

- **Fault injection**: apply-after-health-gate sequencing; 12/35 handlers wired with real
  K8s mutations, 23 deferred with typed reasons. Two real security vulns found & fixed
  (JSON-structure injection, shell injection) with regression tests. Per-RPC `attempt_id`
  ownership enforcement across `InjectFault`/`Connect`/`Destroy`/`MintValidatorCredentials`/
  `ExecValidator`/`ExecShell` (see `PLAN_RPC_AUTHZ.md`).
- **`blast_radius`** forbidden-command detection at the telemetry tap; scored by Dev B.
- **T2 microVM tier**: tier-aware provisioning (`resolveTier()`, `applyT2PodShape`, PSS
  levels, quotas), Kata RuntimeClass manifest, capacity model. Gated behind
  `ORCHESTRATOR_T2_ENABLED` (off by default per PLAN.md's own sequencing gate — no T2 until
  the zero-orphan soak passes). Named security-regression test:
  `TestResolveTier_T2NeverSilentlyDowngradesToT1`.
- **mTLS** on the gRPC server (`RequireAndVerifyClientCert`, self-signed dev CA, 6-scenario
  handshake suite) layered with the shared-secret interceptor. `orchestrator-netpol.yaml`
  dry-run-validated (not live-testable locally — orchestrator runs as a host process here).
- **Dev B**: 35 faults authored; `diagnostic_efficiency` / `hypothesis_ordering` process
  signals; `NO_REGRESSION` + `HTTP_SLO` validators; incident-note artifact +
  `rub.incident-note.v2` AI rubric (100% human review); `sp.production-sim.default` profile;
  Elo calibration engine; retry/cooldown policy; SRE track (all 4 curriculum topics covered).

### Phase 3 — Projects + T3 Cloud Sandboxes — 🟡 CODE-COMPLETE, GATED, NOT RUN ON REAL CLOUD

**Stage 0 (Gate & Contracts)** — mostly done:
- Load harness + namespace-churn soak committed and dry-run/small-run verified (both found
  real bugs — synthetic non-UUID `attempt_id`s failing the ownership check — since fixed).
- Contract additions: `SnapshotManifest` + `SnapshotResponse.manifest` +
  `RestoreRequest.cloud_account_hint` (proto, MINOR); 5 new event types + schemas;
  `activity_spec.schema.json` `mode: PROJECT` support; migrations `0010_project_mode`
  (practice-core) + `0005_cloud_account` (orchestrator), both applied & idempotent.
- **`[B]`**: AWS account-quota ticket, the real multi-node 3×-peak load + soak runs, and the
  **reviewer sign-off on `PHASE1_MVP_COMPLETION.md` §8 (HARD GATE)** — no T3 compute build
  proceeds past the gate.

**Stage 1 (AWS Foundation & Project Skeleton)** — 4/9 `[x]`, 5 `[B]`:
- `[x]` Per-learner Git hosting (**Forgejo deployed locally & verified end-to-end** — real
  `git push` over HTTP, milestone commits, requirements-pack seeding); production Helm +
  Terraform authored.
- `[x]` ClickHouse (**deployed locally & verified** — schema + MergeTree + AggregatingMV +
  rollup); ClickHouse Cloud Terraform authored.
- `[x]` Project-mode milestone state machine (`ProjectService`, `ProjectController`,
  `DefenceService` transport).
- `[x]` `git.service.ts` / `ForgejoClient`.
- `[x]` Early T3 validator executors (`IAC_STATE`, `CLOUD_ASSERT`, `STATIC_ANALYSIS`) —
  tested against real `terraform` + `tfsec`.
- `[x]` `rub.architecture.v3` + calibration harness (runs in FakeAiGrader plumbing mode).
- `[B]` `infra/aws-org/` (Organization + OUs + CloudTrail + Config + GuardDuty),
  `infra/aws-org/scp/` (6 SCP documents + red-team script), `infra/account-baseline/`
  (`LearnerSandboxRole` OIDC trust + `PlatformNukeRole` + require-tags deny) — all
  `tofu fmt`/`validate` clean; **apply needs a real AWS Organizations payer account**.
- `[B]` CLOUD_ASSERT live PASS/FAIL (needs real AWS creds); `rub.architecture.v3` κ≥0.6 SME
  run (needs `ANTHROPIC_API_KEY` + ~2-wk SME pass).

**Stage 2 (Account Lifecycle & Credential Brokering)** — code-complete + unit-tested, real-AWS
runs `[B]`, gate unsigned:
- `credbroker`, `cloudnuke` + `infra/nuke/`, `cloudbudget` + `infra/budget/`, `accountpool`,
  `cloudcost` + `infra/cost/` — all build, full orchestrator suite green, each tested against
  real Postgres (testcontainers) + `cloudaws.FakeClient`. Real path wired behind
  `CLOUD_ACCOUNTS_ENABLED=true` + `AWS_*`/`PLATFORM_*` env.

**Stage 3 (T3 Driver, Editor & Full Evaluation)** — code-complete + tested; `server.go` RPC
wiring + real-T3 runs `[B]`:
- `t3driver`, `snapshotstate`, the 3 remaining validator executors (`TEST_SUITE`,
  `PERF_BENCH`, `CHAOS_PROBE`), defence viva (`RealVivaModel` / `FakeVivaModel`,
  `rub.reasoning.v1`), `sp.project.default` scoring (structural 40% AI cap) — all built &
  tested.
- **Remaining**: replace the `Unimplemented` stubs in `internal/orchestrator/server.go` with
  calls into `t3driver` / `snapshotstate` and add a `TIER_T3_CLOUD_ACCOUNT` `Provision`
  branch (a change to the Phase-1 critical path, deliberately deferred behind the 0.6 gate),
  then run against real cloud.
- `infra/images/openvscode/Dockerfile` authored, **not built/pushed**.

**Stage 4 (Analytics, Dashboard & Hardening)** — backend done, UI + scale runs `[B]`:
- `[x]` ClickHouse ingestion + query migration (ClickHouse-or-Postgres runtime toggle,
  verified `deepEqual` between the two backends); admin cost-dashboard API; Sweeper
  hardening + quarantine runbook.
- `[B]` Cost dashboard UI, T3 workspace chrome (OpenVSCode iframe), milestone tracker +
  viva chat UI (no `web/` work in scope this pass); T3-scale teardown soak; chaos-day.
- `[ ]` Closure cross-check — cannot pass until the gate is signed, `server.go` wiring
  lands, and the T3-scale soak passes on real infra.

### Phase 4 — AI Mentor + Adaptive Engine — ❌ NOT STARTED (product roadmap)
`/ai-gateway` is a README only. Mentor Service, persona/disclosure-ceiling policy engine,
hint-escalation contract, prompt versioning + adversarial CI, the full four-stage ML
recommender, spaced repetition, A/B framework, AI-assisted authoring — none built.
*(Note: `PLAN_PHASE4.md` — the 17-item UI-component-library / utility-extraction checklist —
is separately marked 17/17 complete; that is the centralization plan, not the AI Mentor.)*

### Phase 5 — Scale, Multi-Cloud, Enterprise — ❌ NOT STARTED (product roadmap)
Azure/GCP sandbox drivers, T0 browser-WASM tier, multi-region cluster sharding, RLS/SSO/SCIM,
certification pipeline, public API/LTI, composable simulation generation — none built.
*(`PLAN_PHASE5.md` — the dead-code sweep + 2 READMEs — is the centralization plan's final
task, marked 3/3 complete.)*

---

## 9. Cross-cutting concerns

| Concern | State |
|---|---|
| **Security / RPC authz** | Shared bearer token (constant-time) + mTLS on every orchestrator RPC; per-RPC `attempt_id` ownership checks (parsed UUID comparison, fail-closed); durable `env.audit_log`; helmet + throttler + `AllExceptionsFilter` on practice-core; SCP framework authored (not applied). Full record in `PLAN_RPC_AUTHZ.md`. |
| **Observability** | Orchestrator Prometheus `/metrics` + `/healthz` (separate port), alert rules + Grafana dashboard JSON, structured slog JSON. practice-core `GET /metrics` (transition + validator-result + duration histograms). `attempt_id` correlation key wired on the orchestrator lifecycle/teardown paths; practice-core request-log correlation still open. |
| **Cost** | 60s `usage_meter` emission; budget evaluator chain 50/80/100/120%; force-destroy at 120%; content-CI cost stage fails over `CI_BUDGET_USD` ($0.08). Phase-3 adds independent Cost Explorer / CUR poll + `cloud_cost_usd` + per-account AWS Budgets + launch cap. |
| **CI** | `.github/workflows/ci.yml` (lint + build per service + `contracts` job), `.github/workflows/content-ci.yml` (self-hosted `[self-hosted, content-ci]` runner: nightly full-library + per-PR changed-activities + `workflow_dispatch`). Runner bootstrap fully scripted (`scripts/ci/bootstrap-content-ci-runner.sh` + `docs/content-ci-runner.md`); **operator must still stand up the VM**. |
| **Local dev** | `docker-compose.yml` — `app` profile brings up all 9 containers healthy (orchestrator + practice-core + web + Postgres + Redis + NATS + k3s + registry + MinIO), one-shot migration + kubeconfig-rewrite jobs. `evaluation/phase1/smoke/run-smoke.sh` proves attempt → provision → real-k3s pod → terminal WS → destroy. |
| **Known local instability** | Twice the shared `practice_engine` DB lost its skill/catalog rows (cause unidentified; re-seed with the seed scripts). A disk-exhaustion event (Docker build cache) wedged the Docker VM once. |

---

## 10. Bottom line

- **Phase 0**: done.
- **Phase 1 (Guided Labs MVP)**: functionally complete and proven end-to-end on a local
  real stack; **not formally closed** — the closure gate needs a real multi-node cluster +
  load budget for the 6–7 numeric exit criteria, a gVisor node, the LTI auth seam, ~28 more
  reference solutions, and a second reviewer's sign-off.
- **Phase 2 (Sims + T2)**: complete; T2 intentionally gated off until the zero-orphan soak
  runs on real infra.
- **Phase 3 (Projects + T3)**: all code written, builds clean, unit/integration-tested
  (incl. Forgejo + ClickHouse deployed and verified locally); **blocked** on (a) a signed
  Phase-1 closure gate, (b) `server.go` T3 RPC wiring, and (c) a real AWS Organizations
  account + multi-node cluster for the SCP apply, credential-broker, account-pool, nuke,
  and scale-soak runs.
- **Phase 4 (AI Mentor)** and **Phase 5 (Scale / Multi-Cloud / Enterprise)**: not started.
- The **centralization/refactor plan** (separate Phases 0–5 in `PLAN_PHASE4.md` /
  `PLAN_PHASE5.md`) is fully complete: shared UI component library, API/service layers,
  utility extraction, exception handling, rate limiting, dependency-vuln fixes, dead-code
  sweep.
