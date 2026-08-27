# Practice Engine — Phased Delivery & Two-Developer Work Split

Source of truth: `Hands_practice-engine-architecture.pdf` (referenced below as "the doc"). This plan does not
redecide anything in the doc — it sequences the doc's Part XIII roadmap into concrete, assignable work and
splits it between two developers along the service boundaries the doc already defines in D6.

## How the split works

**Dev A = Execution & Infrastructure track.** Owns everything that touches the runtime environment: the
Environment Orchestrator (Go), execution tiers (T0–T3), security/isolation, cost metering, and the WS
Gateway/Session Broker. This is Part V, Part IX, Part X, and the orchestrator half of Part VIII.

**Dev B = Product & Content track.** Owns Practice Core (catalog, attempts, skills, recommendation), the
content pipeline (activity spec, CMS, content CI), the Evaluation Service, and the AI Gateway/Mentor. This is
Part I–IV, Part VI, Part VII, and the core-services half of Part VIII.

**Why this axis, not frontend/backend or vertical-slice** (per the doc's own D6 reasoning): the Environment
Orchestrator is already a separately-deployed service with its own language (Go), its own credentials, and its
own failure blast radius — nobody else's code needs to change when its internals change, only the gRPC
contract. Practice Core's modules (catalog/attempt/skill/recommendation) share transactional boundaries and
are meant to stay in one deployable, so keeping them with one owner avoids splitting a single transaction
across two people's PRs. The frontend is deliberately **not** assigned as a third silo — it consumes both
tracks' APIs, so each dev builds the UI surface for their own slice (Dev A: workspace/terminal chrome; Dev B:
catalog/dashboard/CMS chrome), which is smaller than it sounds because the doc specifies one unified Activity
Runtime UI (§1.1), not three.

**Conflict-minimizing rules for both devs:**
- The only files both devs write to are: `activity_version.spec_jsonb` schema (frozen at the end of Week 1,
  changes go through a shared PR both must approve), the gRPC/event contracts in `contracts/` (proto + event
  taxonomy — same rule), and `attempt` table DDL (Dev B owns it; Dev A only adds columns via migration, never
  edits Dev B's migration files).
- Every cross-track dependency is called out explicitly below as an **[INTEGRATION POINT]**. Anything not
  marked as one is safe to build independently against a mocked contract.
- Both devs build against the frozen contract from day one using fakes (a fake Orchestrator gRPC server for
  Dev B; a fake Practice Core callback endpoint for Dev A) so integration is a swap-the-mock exercise, not a
  first-contact exercise, at each phase's midpoint checkpoint.

---

## Phase 0 — Contracts & Scaffolding (before Phase 1 starts, ~1 week, both devs together)

This is the one phase both devs do jointly, because it produces the artifacts that let them separate for
everything after.

| # | Deliverable | Notes |
|---|---|---|
| 1 | `contracts/orchestrator.proto` | `Provision / Connect / Snapshot / Restore / Validate / Destroy` (§5.1, §8.1) |
| 2 | `contracts/events.md` + JSON Schema for `attempt_events.payload` | Full taxonomy from §4.2 |
| 3 | `contracts/activity_spec.schema.json` | From §3.2 YAML shape, JSON-Schema'd for CI lint |
| 4 | Postgres schema skeleton, schema-per-bounded-context (`content`, `learner`, `attempt`, `skill`, `env`, `billing`, `admin`) | §8.4 — created empty, each dev migrates their own schemas after |
| 5 | Repo layout: `/practice-core`, `/orchestrator`, `/evaluation`, `/ai-gateway`, `/content`, `/web`, `/contracts` | Mirrors D6 service boundaries 1:1. **As built:** `/orchestrator` is a real separate service; `/evaluation` is a deliberately-bounded module inside `/practice-core` (transactional coupling — see `evaluation/README.md`, boundary enforced by `practice-core/eslint.boundaries.mjs`); `/ai-gateway` is not built (Phase 4 scope — see `ai-gateway/README.md`). |
| 6 | CI skeleton: lint + build per service, no deploy yet. **As built:** also a `contracts` job (`buf lint` + `buf breaking` + stub-freshness) and a self-hosted `content-ci` job (nightly + per-PR) — see `.github/workflows/`. | |
| 7 | Local dev environment: docker-compose with Postgres, Redis, NATS, a single-node k3s (for Dev A's early T1 work) | |

**Exit criteria:** both devs can run `docker-compose up` and hit a stub gRPC call end-to-end (Practice Core →
fake Orchestrator → returns `READY`).

---

## Phase 1 — MVP: Guided Labs, one track (~4 months, per doc §13.1)

Scope per the doc: T1 only, one course track (DevOps: Linux/Docker/Kubernetes), 25–35 guided labs L1–L3, full
evidence→score→mastery pipeline, no AI mentor (static hints only), rules-only recommendation.

### Dev A — Execution & Infrastructure

| Milestone | Tasks | Doc ref |
|---|---|---|
| M1.1 T1 runtime | gVisor RuntimeClass, node pool, namespace-per-environment template (ResourceQuota, LimitRange, NetworkPolicy default-deny, PodSecurity restricted) | §5.2 |
| M1.2 Orchestrator core | Go service implementing `Provision/Connect/Destroy` against T1 driver only; warm-pool CAS in Redis; cold-provision path | §5.5 |
| M1.3 Fixture apply | Idempotent, ordered, checksummed fixture application step in the provisioning pipeline | §5.5 step 3 |
| M1.4 Health gate | Blueprint self-check before `READY` | §5.5 step 4 |
| M1.5 Session Broker + telemetry tap | PTY proxy, server-side command capture, asciicast recording to S3, reconnect/scrollback | §5.4, §4.2 |
| M1.6 WS Gateway | Stateless, JWT/session auth per socket, attempt-scoped authz, rate/backpressure | §8.1 |
| M1.7 Reaper | `environment_reaper` table + 60s force-destroy job; orphan sweep | §5.6 |
| M1.8 Idle/TTL clocks | Two-signal idle detection (silence + low CPU), TTL warnings, long-running-op suppression | §5.6 |
| M1.9 Cost meter | Per-environment usage_meter emission every 60s; budget evaluator chain (50/80/100/120%) | §10.4 |
| M1.10 Egress proxy | Default-deny + allowlist (registry mirror, package mirrors), DNS constraint | §9.2 |
| M1.11 Image strategy | Base images (ubuntu-tools, python-ds, node) + DaemonSet pre-pull top 20 | §5.2 |
| M1.12 Frontend: workspace shell | xterm.js + WebGL renderer, terminal WS wiring, reconnect banner, degraded/offline mode | §8.5 |
| M1.13 Frontend: Monaco editor surface | File API-backed, Phase-1-simple per §5.4 | §8.5 |
| M1.14 Security baseline audit | gVisor confirmed, NetworkPolicy tested, PSS restricted enforced, audit log wired | §9.1, §9.5 |

### Dev B — Product & Content

| Milestone | Tasks | Doc ref |
|---|---|---|
| M1.1 Schema + Practice Core skeleton | `attempt`, `activity`/`activity_version`, `skill`, `skill_edge`, `topic_skill`, `activity_skill` tables + migrations | §8.4 |
| M1.2 Content-as-code pipeline | YAML spec → JSONB projection; `practice-cli validate/test/publish` | §3.2, §3.6 |
| M1.3 Content CI | Lint → provision (calls Dev A's Orchestrator, via contract) → golden path → null path → flake ×5 → timing → cost | §3.5 |
| M1.4 Skill graph (~80 skills, DevOps track) | Edge types (REQUIRES/BUILDS_ON/SIBLING/SPECIALIZES/SUPERSEDES), materialised `skill_closure` via recursive CTE | §2.1–2.3 |
| M1.5 Curriculum mapping | Course→Module→Topic→Subtopic, `topic_skill` join table for the DevOps track | §2.2 |
| M1.6 Attempt lifecycle service | Both state machines (content + attempt), eligibility+quota checks, idempotency keys | §4.1 |
| M1.7 Event store | `attempt_events` append-only partitioned table + replay tool (rebuild `attempt_task_state` from events) | §4.2, §4.4 |
| M1.8 Validator Runner + typed catalogue | `SHELL_ASSERT`, `SHELL_JSON`, `FILE_EXISTS/CONTENT`, `FILE_PARSE`, `K8S_ASSERT` (subset needed for L1–L3 labs) | §6.2 |
| M1.9 Scoring engine | Signals→criteria→profile pipeline; `sp.guided-lab.default` profile only | §6.4 |
| M1.10 BKT mastery engine | 4-param model, evidence update, decay-at-read, mastery bands | §2.4 |
| M1.11 Rules-only recommendation | Candidate gen (curriculum-adjacent + remediation only) → eligibility filter → simple scoring, no ML | §2.5 (reduced) |
| M1.12 Static hint ladder | Authored hints only, penalty tracking, no AI mentor | §7.5 (static subset) |
| M1.13 Frontend: catalog, dashboard, task checklist, results page | Home/Catalog/Skills/History nav (§1.2), workspace task panel | §1.2, §8.5 |
| M1.14 Admin: minimal CMS (read/preview) | Authoring stays in Git for Phase 1; CMS is preview + publish-request only | §3.7 |
| M1.15 Basic admin analytics | Postgres-read-replica-backed, no ClickHouse yet | §13.1 |

### [INTEGRATION POINTS] — Phase 1
1. **Provision contract**: Dev B's Attempt Service calls Dev A's Orchestrator via the Phase-0 gRPC contract.
   Dev B builds against a fake Orchestrator (returns `READY` after a fixed delay) until Dev A's is live.
2. **Validator Runner credentials**: Dev A's Orchestrator must mint short-lived read-only K8s creds on request
   from Dev B's Validator Runner (§6.2 execution rules). Contract: a `MintValidatorCredentials(env_id, ttl)`
   RPC — add to `contracts/orchestrator.proto` in Phase 0.
2b. **Command telemetry consumption**: Dev A's Session Broker emits `COMMAND_EXECUTED` events; Dev B's event
   store is the sink. Agree on the NATS subject naming in Phase 0 (`env.telemetry.*`).
3. **Cost meter → budget enforcement**: Dev A emits `usage_meter` rows; Dev B's (or shared) budget evaluator
   reads them. Decide ownership explicitly — recommend Dev A owns emission, Dev B owns the evaluator/UI, table
   schema fixed in Phase 0.
4. **Reaper ↔ attempt state**: when Dev A's reaper force-destroys an environment, it must publish an
   `ENV_DESTROYED` event Dev B's Attempt Service consumes to transition attempt state. Fixed event contract,
   no direct DB writes across the boundary.

**Phase 1 exit criteria (from the doc, §13.1):** 200 learners complete ≥3 labs; provision success ≥99%;
time-to-ready p95 ≤20s; validator ERROR rate <0.5%; cost/attempt <$0.08; measured Elo available per lab.

---

## Phase 2 — Production Simulations + T2 (~3 months)

### Dev A
- T2 microVM tier: Firecracker/Kata driver, DinD, k3s multi-node, systemd/eBPF support (§5.1, §5.3 alt.)
- Fault injection mechanism: apply-after-health-gate sequencing, fault manifest execution (§1.3.2, §3.3)
- `blast_radius` forbidden-command detection at the telemetry-tap layer (§3.3)
- Second cluster/node-pool capacity planning for T2 workloads

### Dev B
- Fault library (first 30 faults) as versioned content primitives (§3.4)
- Process telemetry signals: `diagnostic_efficiency`, `hypothesis_ordering` scoring (§3.3, §6.4)
- `NO_REGRESSION` and `HTTP_SLO` validator types (§6.2)
- Incident-note artifact + first AI-graded rubric (`rub.incident-note.v2`) — **first use of Dev B's AI Gateway
  integration**, human-reviewed at 100% initially (§6.5, §13.1)
- `sp.production-sim.default` scoring profile (§6.4)
- Elo calibration engine going live (§2.6)
- Retry/cooldown policy (§2.7)
- Second course track content authoring

### [INTEGRATION POINTS] — Phase 2
- Fault application is triggered by Dev B's Attempt Service but executed by Dev A's Orchestrator
  (`InjectFault(env_id, fault_spec)` RPC — extend the Phase-0 contract).
- `blast_radius` forbidden commands are detected in Dev A's Session Broker tap but scored by Dev B's scoring
  engine — same event-based handoff as Phase 1's `COMMAND_EXECUTED`, no new mechanism needed.

**Dependency note:** The doc's explicit zero-orphan gate is stated for T3 (§13.1 Phase 3), not T2. It is
applied here to T2 as well because the same teardown-discipline and namespace-churn risks (R1, and R9's
"load test namespace churn at 3× projected peak before Phase 3") already bite at the T2 microVM tier — Dev A
should not start T2 until Phase 1's reaper/teardown has run with zero orphans for a sustained period. This is
a plan-level sequencing decision derived from the doc's risk register, not a verbatim doc requirement.

---

## Phase 3 — Projects + T3 Cloud Sandboxes (~4 months)

### Dev A
- AWS account vending: OU structure, Account Pool Manager (AVAILABLE/IN_USE/NUKING/QUARANTINED) (§5.3)
- SCP framework (region/service denies, org-boundary denies, public-sharing denies) (§5.3)
- Credential brokering: STS AssumeRoleWithWebIdentity via OIDC, sidecar refresh, revoke-on-state-change (§5.3, §9.3)
- Nuke + mandatory verification step (`aws-nuke` via `PlatformNukeRole`) (§5.3)
- Independent Cost Explorer/CUR poll alongside AWS Budgets (§5.3, §10.4)
- T3 driver for the Orchestrator interface
- OpenVSCode Server integration (replacing Monaco for T3) (§5.4)

### Dev B
- Project mode: milestone gates (design→infra→implementation→hardening→final), each with its own
  validator/rubric slice (§1.3.3, §12.3)
- Platform-hosted Git server provisioning per learner (§1.3.3)
- `IAC_STATE`, `CLOUD_ASSERT`, `TEST_SUITE`, `STATIC_ANALYSIS`, `CHAOS_PROBE`, `PERF_BENCH` validator types (§6.2)
- Architecture rubric + defence viva question generator (from learner's own design doc/commits) (§1.3.3, §12.3)
- `sp.project.default` scoring profile
- ClickHouse analytics pipeline stood up (migration off Postgres-read-replica) (§8.4, §13.1)
- Full admin cost dashboard (cost per learner/course/activity) (§10.3, §11.3)

### [INTEGRATION POINTS] — Phase 3
- Suspend/resume for long-running projects: Dev B's Attempt Service calls Dev A's `Snapshot`/`Restore` RPCs
  (already in the Phase-0 contract, now exercised for the first time at the IaC-state level — Terraform state
  pull, `kubectl get -A` filtered, cloud resource inventory). Confirm the snapshot payload shape together
  before either side builds against it.
- Cost dashboard (Dev B) consumes `usage_meter`/`cloud_account` data Dev A's account pool manager emits —
  same pattern as Phase 1's cost integration point, now including cloud_cost_usd.

**Dependency note (doc, §13.1):** "Do not build T3 until orphan-environment count has been zero for a
sustained period." This is a hard gate on Dev A's Phase 3 start, independent of Dev B's readiness.

---

## Phase 4 — AI Mentor + Adaptive Engine (~3 months)

This phase is where the AI Gateway (owned jointly at the contract level, but let's assign an owner) and the
full recommender land. Per the doc: "the mentor is only as good as the environment-state context it can
retrieve" — so this genuinely depends on Phase 1–3 being solid, not just sequenced for convenience.

### Dev A
- LLM Gateway infra concerns: multi-provider failover/health-check, budget circuit breaker, caching layer
  (exact-match + semantic) (§7.6)
- Environment-state summary API: structured, on-demand read of validator results / recent commands / resource
  summary, exposed to the Mentor Service (§7.4) — this is Dev A's environment data, packaged for Dev B's
  Mentor Service to consume without direct access
- IAM boundary enforcement: separate storage bucket/index/service identity so Mentor's credentials structurally
  cannot read `reference_solution` (§7.4, §7.7) — this is a security-infra concern, sits naturally with Dev A

### Dev B
- Mentor Service: policy resolution, intent classification, context assembly, output guardrails (§7.2–7.5)
- Persona/disclosure-ceiling policy engine per mode (§7.3)
- Hint escalation contract incl. "just tell me" guided-fallback path, assisted-flag propagation to BKT (§7.5)
- Prompt versioning + adversarial CI suite (solution-leakage tests) (§3.5 step 8, §7.7)
- Full four-stage recommender: candidate gen (all 5 sources) → eligibility → weighted scoring → re-rank/package
  with reason codes (§2.5)
- Spaced repetition scheduling (§2.4 review-due, §2.5 f5)
- Auto-generated remediation ladder (skill-DAG walk) (§2.7)
- A/B experimentation framework + north-star metric instrumentation (§11.4)
- AI-assisted content authoring tool (internal use, highest-ROI per doc §3.1)

### [INTEGRATION POINTS] — Phase 4
- Mentor Service (Dev B) calls Dev A's environment-state summary API and LLM Gateway routing/budget layer —
  clean API boundary, define the summary payload schema in a joint session before either side builds.
- The IAM boundary (Dev A) is enforced at infra level but Dev B's Mentor Service code must be written to never
  attempt the read — this needs a short joint design review, not ongoing coordination.

---

## Phase 5 — Scale, Multi-Cloud, Enterprise (~4 months)

### Dev A
- Azure (subscription-per-learner, Azure Policy, Entra Workload Identity) and GCP (project-per-learner,
  Org Policy, Workload Identity Federation) sandbox drivers, added at the Orchestrator interface level only (§5.3, §13.1)
- T0 browser-WASM tier (Pyodide, sql.js/DuckDB-WASM) (§5.1)
- Multi-region practice cluster sharding (~2,000 concurrent envs/cluster) (§5.7, §13.3)
- Enterprise compute isolation: dedicated node groups, per-tenant KMS keys, network-layer tenancy (§5.7)

### Dev B
- Enterprise data/tenancy: RLS enforcement audit, SSO/SCIM, custom content per tenant (§5.7, §8.4)
- Certification pipeline with proctoring option (§13.1)
- Cohort management surface for instructors
- Public API + LMS/LTI integration
- Composable simulation generation: blueprint × fault-set selection driven by weak-skill targeting (§3.4, §13.1)

### [INTEGRATION POINTS] — Phase 5
- Multi-tenant quota enforcement spans both: Dev A enforces compute/cloud-account tenancy, Dev B enforces
  data/RLS tenancy. Both read from the same `tenant_id`-scoped `budget` table (already shared since Phase 1).

---

## Cross-cutting ownership (applies every phase)

| Concern | Owner | Why |
|---|---|---|
| `contracts/` (proto + event schema + activity spec schema) | Joint — PR requires both approvals | Changes here break the other side silently if unreviewed |
| Postgres schema-per-bounded-context | `env`/`billing` schemas: Dev A. `content`/`learner`/`attempt`/`skill`/`admin` schemas: Dev B | Matches D6; each dev migrates only their own schemas |
| Observability (OTel → Prometheus/Loki/Tempo/Grafana) | Joint setup once in Phase 0/1, then each dev instruments their own services | `attempt_id` as universal correlation key must be wired by both from day one (doc §13.5 #1) |
| Security review / threat model (Part IX) | Dev A leads (infra-heavy), Dev B contributes T8/T9 (prompt injection, cheating) items | Threat categories split cleanly along the same service line |

## Weekly integration checkpoint (recommended cadence)

Given the doc's own emphasis on contracts-first design, the lowest-friction integration rhythm is: both devs
merge to `main` behind their own service boundary continuously; a joint 30-minute checkpoint each week
specifically walks the **[INTEGRATION POINT]** list for the current phase and confirms no contract has drifted.
Save any contract change for that checkpoint rather than making it ad hoc mid-week.
