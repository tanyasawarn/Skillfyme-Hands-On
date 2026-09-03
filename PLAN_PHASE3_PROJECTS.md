# PLAN_PHASE3_PROJECTS.md — Phase 3: Projects + T3 Cloud Sandboxes

**Status: NOT STARTED (~5%). This is a pre-implementation build-out plan, not an implementation.**

Source of truth: `PLAN.md` §"Phase 3 — Projects + T3 Cloud Sandboxes" (lines 157–188) and
`memory.md` §13.1 (lines 2186–2194), §5.3 (Tier 3 detail), §6.2 (validator catalogue),
§1.3.3 / §12.3 (project mode + viva), §10.3–10.4 (cost), §8.4 (ClickHouse).

> **Note on `PLAN_PHASE3.md`:** that file is misnamed. It tracks a completed "Infrastructure
> Boilerplate Consolidation" refactor (12/12 done: `getOrNotFound`, `RunTicker`, `Role` enum,
> `BaseGrpcClient`, …) against a "PLAN.md lines 312-315" section that never existed in this repo
> (PLAN.md has always been ~260 lines). It has nothing to do with the Phase 3 in this document.
> Recommend renaming it to `REFACTOR_TRACKER_BOILERPLATE.md`. `PHASE3_STANDARDIZATION.md` is
> likewise a generic hardening checklist, not Phase 3 feature scope.

---

## 0. Scope validation result

The proposed Phase 3 scope **accurately reflects the documented requirements**, with three
additions the summary dropped and one reclassification.

### 0.1 Confirmed in scope (matches docs exactly)

| Area | Item | Doc ref |
|---|---|---|
| **Infra (Dev A)** | AWS account vending — Organization + OU structure (`Platform` / `ContentCI` / `LearnerSandboxes`) | §5.3 |
| | Account Pool Manager — states `AVAILABLE` / `IN_USE` / `NUKING` / `QUARANTINED`, per-region warm pool | PLAN.md 160, §5.3 |
| | SCP framework — region denies, expensive-SKU denies, org-boundary denies (`organizations:*`, `account:*`, leave-org, CloudTrail/Config/GuardDuty tamper, delete nuke role), IAM-user/no-expiry-key denies, public-S3/AMI/snapshot denies, SES/SNS-SMS denies | §5.3 |
| | STS credential brokering — `AssumeRoleWithWebIdentity` via OIDC federation, subject = attempt ID, max-1h session, sidecar auto-refresh, refresh stops the instant attempt leaves `IN_PROGRESS`, revoke-on-state-change | §5.3, §9.3, D9 |
| | Environment cleanup — `aws-nuke` / `cloud-nuke` from platform account assuming `PlatformNukeRole` (SCP-undeletable); runs on TTL, on submit, on idle, nightly sweeper | §5.3 |
| | **Mandatory verification layer** — post-nuke resource enumeration via Config / Resource Explorer; non-empty ⇒ `QUARANTINED` + page; never straight back to pool | §5.3 |
| | Independent Cost Explorer / CUR hourly poll alongside AWS Budgets (Budgets lag by hours) | §5.3, §10.4 |
| | T3 driver implementing the Orchestrator interface (`Provision`/`Connect`/`Snapshot`/`Restore`/`Validate`/`Destroy`) | PLAN.md 165, line 928 |
| | OpenVSCode Server — replaces Monaco for T3 only, ~500 MB RAM/env, gated by tier + activity | §5.4, lines 1043 / 1689 |
| **Backend (Dev B)** | Project mode — milestone gates `design → infra → implementation → hardening → final`, each with its own validator + rubric slice | PLAN.md 169, §12.3 |
| | Defence viva — 6–8 questions generated from the learner's **own** design doc + **own** commit history, scored on a reasoning rubric, human-reviewed for certification | PLAN.md 173, §12.3, line 1118 |
| | Architecture rubric — `rub.architecture.v3` ("appropriateness given stated constraints", not "textbook answer") | §12.3 |
| | `sp.project.default` scoring profile | PLAN.md 174 |
| | Validator types — `IAC_STATE`, `CLOUD_ASSERT`, `TEST_SUITE`, `STATIC_ANALYSIS`, `CHAOS_PROBE`, `PERF_BENCH` (acceptance probes, chaos testing, perf benchmarking are the *execution layer* for these, not separate workstreams) | §6.2 |
| | Platform-hosted Git server — provisioned **per learner**, repo retained permanently (portfolio value) | PLAN.md 171, §12.3 |
| | ClickHouse analytics pipeline — migration off the Postgres read-replica | §8.4, line 2221 |
| | Full admin cost dashboard — cost per learner / course / activity | §10.3, §11.3 |
| **Data/observability** | ClickHouse integration | §8.4 |
| | Cost dashboard | §10.3 |
| **Dev environment** | OpenVSCode (cloud IDE) | §5.4 |

### 0.2 Additions the summary dropped (documented, in scope, must be planned)

1. **`sp.project.default` scoring profile** (Dev B) — PLAN.md 174. Milestone scores roll up
   into this; without it the milestone gates produce no mastery signal.
2. **Budget enforcement chain for T3** (Dev A) — `memory.md` 2188, 2192. Per-account AWS
   Budgets at 50/80/100 % of the activity's declared budget → EventBridge → orchestrator; at
   100 % the orchestrator revokes credentials + force-terminates. Plus a launch cap on
   concurrent T3 attempts. This is distinct from the Cost Explorer poll (which is
   *observation*); this is *enforcement*.
3. **Long-lived suspension via `Snapshot` / `Restore` at IaC-state level** (integration
   point, both devs) — PLAN.md 179–182. First real exercise of the `Snapshot`/`Restore`
   RPCs: Terraform state pull, filtered `kubectl get -A`, cloud resource inventory. Snapshot
   payload shape must be agreed jointly before either side builds against it.

### 0.3 Reclassification

"Acceptance probes / chaos testing / performance benchmarking" and "rubric-based assessment /
defence viva system" are **not** separate workstreams. They are:

- Acceptance/chaos/perf = the execution layer for `TEST_SUITE` / `CHAOS_PROBE` / `PERF_BENCH`
  (one deliverable: the T3 validator executors).
- Rubric assessment + viva = one deliverable each (`rub.architecture.v3` +
  `rub.reasoning.v1`, and the viva question generator + transcript scorer).

Treat as one line each to avoid double-counting effort.

### 0.4 Out of scope for Phase 3 (explicit — do not build)

- Azure / GCP sandbox drivers — Phase 5 (§13.1; R12: "AWS-only through Phase 4").
- T0 browser-WASM tier — Phase 5.
- AI Mentor / adaptive recommender — Phase 4. The viva question generator is a *content
  generator* run at grading time, not the Mentor Service; it must not depend on Phase 4's
  LLM Gateway routing/budget layer (it can call the model directly, same as the Phase 2
  `rub.incident-note.v2` grader already does).
- Multi-region practice-cluster sharding — Phase 5 (§13.3). T3 account pools are
  per-region but the practice cluster stays single.

---

## 1. Current state assessment (verified against code, 2026-08-27)

| Claimed gap | Verification |
|---|---|
| No T3 orchestration driver | `orchestrator/internal/k8s/provision.go:85-100` — `Tier` enum deliberately defines only `TierT1SharedContainer` / `TierT2IsolatedMicroVM`; doc comment: "TIER_T3_CLOUD_ACCOUNT has no K8s-side driver at all… T3 is Phase 3 scope". |
| No AWS account vending / SCP / STS / aws-nuke | Zero matches for `CostExplorer`, `AssumeRoleWithWebIdentity`, `AccountPool`, `aws-nuke`, `PlatformNukeRole` anywhere in `*.go` / `*.ts`. |
| `Snapshot` / `Restore` RPCs return `Unimplemented` | `orchestrator/internal/orchestrator/server.go:619-624` — literally `status.Error(codes.Unimplemented, "snapshot/restore is Phase 3 scope (project mode workspaces)")`. Proto messages + client stubs exist (`pkg/pb/…`). |
| Validator types are enums only, no execution layer | `server.go:1054` returns `Unimplemented, "no real executor implemented for validator type %s yet"`. `practice-core/src/modules/evaluation/validator-executor.interface.ts:26-32` and `catalog/activity-spec.ts:137-143` list `CLOUD_ASSERT`/`IAC_STATE`/`TEST_SUITE`/`STATIC_ANALYSIS`/`PERF_BENCH`/`CHAOS_PROBE` as string-union types with no dispatch. |
| No project mode / milestone tracking | `PROJECT` exists only as `ActivityMode` literal (`db/schema.ts:101`); `evaluation.service.ts:28` references a `sp.project.default` profile that isn't built. No `milestone` / `viva` / `defence` implementation code (only doc-comment mentions). No `project_milestone_state` / `project_submission` tables (schema names appear in `memory.md` line 1615 only). |
| No viva / evaluation-for-projects system | Confirmed — no code. |
| No platform Git server | Only `orchestrator/internal/fixture/handlers_gitea.go` — a **single shared in-cluster Gitea** used as a *fault-injection scenario prop* for sim activities (branch-protection incidents etc.). This is **not** the per-learner platform Git hosting Phase 3 requires. Do not assume it is done. |
| No ClickHouse | Only a doc-comment in `practice-core/src/modules/admin/analytics.service.ts:26-28` ("Postgres-read-replica-backed, no ClickHouse yet… Phase 3+"). |
| Only artifact: `PHASE3_STANDARDIZATION.md` | Confirmed unrelated — generic component-library / exception-filter / rate-limit hardening checklist. |

**The ~5 % that exists** is forward-compatible contract surface only: the `Snapshot`/`Restore`
proto + Go/TS stubs, the `TIER_T3_CLOUD_ACCOUNT` enum value, the six validator-type string
unions, the `PROJECT` activity-mode plumbing, and the four-method Orchestrator interface
abstraction. Nothing behind any of them is implemented.

---

## 2. HARD GATE — do not start T3 build

`memory.md` 2190 & `PLAN.md` 186–187 (verbatim doc requirement, not a plan-level derivation):

> **"Do not build T3 until orphan-environment count has been zero for a sustained period —
> the same bug that leaks a pod leaks a NAT gateway."** This is a hard gate on Dev A's Phase 3
> start, independent of Dev B's readiness.

### Gate status: **NOT MET.**

From `PHASE1_MVP_COMPLETION.md`:

- §7 line 303: *"Zero orphan environments sustained during + 1h after the run —
  `reaper_orphans_found_total` == 0"* is marked **`[B]`** (blocked — needs a real run).
- §3.4 line 184: *"namespace churn at 3× projected peak soak (PLAN.md R9) — script + pass
  criteria (zero orphans)"* is **`[ ]`** (not done — harness unwritten).
- §3.4 line 185–186: the 200-learner load run is **`[B]`** — not executed on a real cluster.
- §7 line 307–313: *"a single-node Docker-Desktop k3s cannot sustain 200 concurrent workspace
  pods, and none of the seven measured artifacts exist yet. The load harness is still
  unwritten."*
- §8: Phase 1 is **not CLOSED** — sign-off "pending", zero of nine closure conditions met.

`PHASE2_CLOSEOUT.md` scoped Phase 2 explicitly to T2 and deferred all T3/AWS items; it does
not add orphan-gate evidence.

### Gate prerequisites (Dev A, before any Phase 3 infra work)

- **G1** Write the load harness (`evaluation/phase1/load/`, k6 or Locust) — 200 concurrent
  learners, provision → 20+ commands → validate → submit → destroy.
- **G2** Write the namespace-churn soak (3× projected peak, PLAN.md R9), pass criterion:
  zero orphans during + 1 h after.
- **G3** Execute both on a real multi-node cluster. Commit
  `evaluation/phase1/results/loadtest-<date>.md` + `reaper_orphans_found_total == 0` sustained
  for the run + 1 h.
- **G4** Run the same soak against the **T2** teardown path (Firecracker/Kata destroy leaves
  no microVM, no CNI leak) — Phase 2's `PHASE2_CLOSEOUT.md` did not measure this under load.
- **G5** Reviewer sign-off recorded on `PHASE1_MVP_COMPLETION.md` §8 and a short
  `PHASE2_TEARDOWN_SOAK.md`.

**Estimated gate effort: 2–3 weeks (Dev A), plus a multi-node cluster.** This is a
prerequisite, not part of the 4-month Phase 3 estimate. Dev B's Phase 3 work (§4 below) is
**not** gated on this and can start in parallel with G1–G5.

---

## 3. Separation of responsibilities

| Layer | Owner | What lives here |
|---|---|---|
| **Infra** (Terraform, AWS Organizations, IAM/STS/OIDC, SCPs, cluster) | Dev A | AWS Org + OUs, the account-baseline Terraform module, `PlatformNukeRole` + nuke runner, OIDC provider, SCP policy documents, EventBridge → orchestrator wiring, T3 node access path (SSM), OpenVSCode container image + node pool sizing. Delivered as IaC in a new `infra/` tree + Go code in `orchestrator/`. |
| **Backend — orchestrator** (Go service) | Dev A | Account Pool Manager state machine, T3 driver behind the `Provision/Connect/Snapshot/Restore/Destroy` interface, STS sidecar broker, budget-enforcement consumer, Cost Explorer poller, `Snapshot`/`Restore` IaC-state implementation, T3 validator credential minting (`MintValidatorCredentials` extended for cloud roles). New `env` + `billing` schema migrations (Dev A owns those schemas). |
| **Backend — practice-core** (NestJS) | Dev B | Project-mode state machine + milestone gates, `project_milestone_state` / `project_submission` tables (`attempt` schema — Dev B owns), per-milestone validator/rubric slice wiring, `sp.project.default` scoring profile, viva question generator + transcript scorer, `rub.architecture.v3` + `rub.reasoning.v1`, the six T3 validator executors' **client-side** dispatch + result recording, ClickHouse ingestion + query layer, cost-dashboard API. |
| **Platform services** (deployed infra, not app code) | Dev A provisions, Dev B consumes | Per-learner Git hosting service (a managed Gitea/Forgejo deployment **or** a thin wrapper over a hosted provider — decision D-P3-1 below), ClickHouse cluster, the analytics event-bus consumer deployment. |
| **Frontend** (`web/`) | split by surface | Dev A: T3 workspace chrome (OpenVSCode iframe surface, cloud-cred status indicator, budget banner). Dev B: milestone tracker UI, viva chat surface, admin cost dashboard. |
| **Contracts** (`contracts/`) | Joint — PR needs both approvals | `Snapshot`/`Restore` payload shape (currently thin), any new RPC (`GetAccountCost`, viva doesn't need one), event-taxonomy additions (`MILESTONE_SUBMITTED`, `MILESTONE_GATED`, `ACCOUNT_CLAIMED`, `ACCOUNT_NUKED`, `ACCOUNT_QUARANTINED`, `DEFENCE_MESSAGE`), `cloud_account` / `usage_meter.cloud_cost_usd` schema. |

**Files both devs touch (freeze rules from `PLAN.md` 27–33 still apply):**
`contracts/orchestrator.proto`, `contracts/events.*`, `contracts/activity_spec.schema.json`
(the six validator types + milestone spec sections), and the `attempt` table DDL
(Dev B owns; Dev A adds columns via migration only).

---

## 4. Component-wise breakdown

Each component lists: **owner · doc ref · depends on · integration points · rough size**
(S ≈ ≤1 wk, M ≈ 1–3 wk, L ≈ 3–6 wk, one dev).

### Track A — Infra & Orchestrator (Dev A)

#### A1. AWS Organization + OU scaffold — `infra/aws-org/` — §5.3 — **M**
- Terraform: Organization, OUs `Platform` / `ContentCI` / `LearnerSandboxes`, delegated
  admin, centralised CloudTrail + Config + GuardDuty in the Platform account (undeletable
  from sandboxes via A2's SCPs).
- Deliverable: `terraform apply` produces the Org; a manual `aws organizations
  create-account` into `LearnerSandboxes` succeeds and inherits the SCPs.
- Depends on: a real AWS payer account + Organizations enabled + a support-ticket quota
  raise to ≥ (peak-T3 × 2) accounts (lead time: **days to weeks** — start this on day 1).
- Integration points: none (pure infra).

#### A2. SCP framework — `infra/aws-org/scp/` — §5.3 — **M**
- Six policy documents matching §5.3 bullet list exactly: region deny, expensive-SKU deny
  (with a blueprint-exception mechanism — an SCP condition keyed on an account tag the
  orchestrator sets at claim time), org-boundary deny, IAM-hardening deny, public-sharing
  deny, mail-abuse deny.
- Deliverable: a red-team script in `infra/aws-org/scp/verify/` that assumes a sandbox role
  and asserts each denied action returns `AccessDenied` (e.g. `ec2 run-instances` in a
  disallowed region, `organizations:LeaveOrganization`, `s3api put-bucket-acl --acl
  public-read`, `iam:DeleteRole` on `PlatformNukeRole`).
- Depends on: A1.
- Integration points: the expensive-SKU exception tag is written by A4 (Pool Manager) at
  claim time from the blueprint's `environment.cloud.sku_exceptions` — new
  `activity_spec.schema.json` field (contract change, joint PR).

#### A3. OIDC provider + role trust + STS broker sidecar — `infra/aws-org/oidc/` + `orchestrator/internal/credbroker/` — §5.3, §9.3, D9 — **L**
- Infra: an OIDC identity provider in each sandbox account (created by A5's baseline
  Terraform, pattern-restricted per SCP), a `LearnerSandboxRole` whose trust policy binds
  `sub` = attempt ID and `aud` = the platform IdP.
- Orchestrator: the platform IdP endpoint issuing short-lived JWTs keyed on attempt ID;
  the **sidecar broker** container that runs next to the T3 workspace, calls
  `AssumeRoleWithWebIdentity`, writes creds to a shared `emptyDir`, refreshes at 50 % of
  TTL, and **stops refreshing the instant the attempt leaves `IN_PROGRESS`** (subscribes to
  the same `ENV_DESTROYED` / attempt-state event stream the reaper uses).
- Deliverable: a T3 workspace can `aws sts get-caller-identity` and see the sandbox role;
  after the attempt is suspended, the next refresh fails and existing creds expire within
  1 h; `aws iam list-users` returns `AccessDenied` (SCP).
- Depends on: A1, A2, A5.
- Integration points: **IP-A3** — attempt-state signal. Dev B's Attempt Service already
  emits attempt-state transitions on NATS (Phase 1 `ENV_DESTROYED` pattern); the broker
  consumes them. No new contract, reuse `env.lifecycle.*`.

#### A4. Account Pool Manager — `orchestrator/internal/accountpool/` — PLAN.md 160, §5.3 — **L**
- State machine `AVAILABLE → IN_USE → NUKING → (AVAILABLE | QUARANTINED)`, backed by a new
  `env.cloud_account` table (Dev A's `env` schema). Redis CAS for claim (same pattern as
  `internal/warmpool`).
- Warm-pool fill loop (reuse `internal/loop.RunTicker`) maintaining N clean accounts/region.
- Claim path: pick `AVAILABLE` → set budget alarm (A7) → apply baseline Terraform (A5) →
  set SCP exception tag (A2) → mark `IN_USE` → hand account ID to the T3 driver (A6).
- Release path: revoke (A3 stop) → `NUKING` → run nuke (A8) → verify (A8) → `AVAILABLE` or
  `QUARANTINED` + page.
- Deliverable: `accountpool_test.go` with a fake AWS layer covering every transition + the
  quarantine-on-failed-verify branch + "never returns a non-verified account". Live: claim
  → release → re-claim the same account cleanly.
- Depends on: A1, A5, A7, A8.
- Integration points: **IP-A4a** emits `ACCOUNT_CLAIMED` / `ACCOUNT_NUKED` /
  `ACCOUNT_QUARANTINED` events (new taxonomy entries, joint PR) that Dev B's cost dashboard
  (B10) and admin alerting consume. **IP-A4b** `cloud_account` + `usage_meter.cloud_cost_usd`
  schema is read by B10 — same pattern as Phase 1's cost integration point.

#### A5. Account-baseline Terraform module — `infra/account-baseline/` — §12.3 M2 — **M**
- The Terraform the Pool Manager applies into a freshly-claimed account: remote-state
  backend (platform-managed S3 + DynamoDB lock **in the Platform account**, so
  destroy/recreate of the sandbox loses nothing — §12.3), OIDC provider (A3), base VPC
  skeleton or nothing depending on blueprint, resource-tagging enforcement
  (`attempt_id` / `tenant_id` required tags via an SCP or a tag policy).
- Deliverable: applies in < 5 min; `terraform destroy` leaves the account empty enough that
  A8's nuke + verify passes.
- Depends on: A1.
- Integration points: the remote-state backend location is what makes B3's milestone-2
  `IAC_STATE` validator ("remote state, no secrets in state") checkable.

#### A6. T3 driver — `orchestrator/internal/t3driver/` + wire into `internal/orchestrator/server.go` — PLAN.md 165, line 928 — **L**
- Implements the driver side of `Provision` / `Connect` / `Destroy` for
  `TIER_T3_CLOUD_ACCOUNT`: `Provision` = claim account (A4) + apply baseline (A5) + start
  the workspace pod (running in the **platform** cluster, not the sandbox — the pod holds
  the editor + CLI + broker sidecar and talks to AWS over brokered creds) + OpenVSCode
  container (A9). `Connect` = OpenVSCode WSS URL + terminal WS. `Destroy` = release path (A4).
- Extend `k8s/provision.go`'s `Tier` enum with `TierT3CloudAccount` and the pod shape
  (adds the broker sidecar + OpenVSCode container; no gVisor needed — the risky code runs
  in the sandbox account, not the pod).
- Deliverable: `Provision` with `tier: TIER_T3_CLOUD_ACCOUNT` returns `READY` with a
  claimed account + a reachable OpenVSCode; `Destroy` returns the account to `AVAILABLE`.
- Depends on: A4, A5, A9.
- Integration points: **IP-A6** — Dev B's Attempt Service calls `Provision`/`Destroy` with
  the new tier via the existing Phase-0 contract. No contract change for the happy path.

#### A7. Budget enforcement chain — `orchestrator/internal/cloudbudget/` — §5.3, §10.4, `memory.md` 2188/2192 — **M**
- Per-account AWS Budgets at 50/80/100 % of `activity_spec.environment.cost_budget_usd`
  (the field already exists — K10 confirmed `insertVersion` reads it). EventBridge rule →
  SNS → an orchestrator HTTP endpoint. At 100 %: call A3-stop (revoke) + A6 `Destroy`
  (force-terminate). At 50/80 %: emit a warning event.
- A **launch cap**: a config'd max concurrent `IN_USE` accounts; `Provision` returns
  `ResourceExhausted` above it (Dev B surfaces "T3 capacity reached, retry shortly").
- Deliverable: a synthetic budget breach (set `cost_budget_usd` to $0.01 on a test
  activity) triggers real credential revocation + teardown within one EventBridge cycle.
- Depends on: A1, A4, A6.
- Integration points: reuses Phase 1's cost-integration pattern (Dev A emits, Dev B's UI
  reads). The launch-cap `ResourceExhausted` is a new response path Dev B handles in
  provisioning.

#### A8. Nuke + mandatory verification — `orchestrator/internal/cloudnuke/` + `infra/nuke/` — §5.3 — **M**
- `PlatformNukeRole` in each sandbox (created by A5, SCP-undeletable per A2). A nuke runner
  (containerised `aws-nuke` with a generated config scoped to the account) invoked by A4's
  release path, **and** a standalone nightly sweeper (cron) that nukes every `AVAILABLE` +
  `QUARANTINED` account regardless of state.
- **Verification step (mandatory, blocking):** after nuke, enumerate via
  AWS Config + Resource Explorer + a hardcoded list of nuke-blind-spot services; any
  non-empty result ⇒ return `QUARANTINED` + page (PagerDuty/Opsgenie webhook).
- Deliverable: nuke an account with a deliberately-planted S3 bucket + EC2 instance +
  IAM role → verification confirms empty; plant a resource in a service `aws-nuke` misses
  (e.g. a Route53 hosted zone if not in the config) → verification catches it → account
  goes to `QUARANTINED`, not `AVAILABLE`.
- Depends on: A1, A5.
- Integration points: **IP-A8** emits `ACCOUNT_QUARANTINED` (taxonomy, joint PR) →
  Dev B admin alerting + an ops runbook link.

#### A9. OpenVSCode Server integration — `infra/images/openvscode/` + T3 pod shape — §5.4, line 1043 — **M**
- A container image: OpenVSCode Server + the cloud CLIs (aws, terraform, kubectl, helm) +
  language servers, ~500 MB RAM budget. Runs as a second container in the T3 workspace pod
  (A6). WSS terminated at the platform, proxied inward over the control-plane channel
  (K8s exec for the terminal; a plain WS proxy for the editor) — never a routable address
  into the pod (line 1040).
- Deliverable: a T3 `Connect` returns a working OpenVSCode with a file tree, integrated
  terminal, and a language server responding; Monaco stays the editor for T1/T2 (no change
  there).
- Depends on: A6.
- Integration points: **IP-A9** — `web/` T3 workspace chrome embeds the OpenVSCode iframe;
  Dev A builds that surface.

#### A10. Independent Cost Explorer / CUR poll — `orchestrator/internal/cloudcost/` — §5.3, §10.4 — **S**
- Hourly poll of Cost Explorer (per-account, grouped by tag) + a daily CUR-in-S3 read for
  reconciliation. Writes `usage_meter.cloud_cost_usd` rows tagged with `attempt_id`
  (one account = one attempt makes this exact — line 1849).
- Deliverable: cloud cost for a finished T3 attempt appears in `usage_meter` within an hour
  and reconciles against the CUR within a day.
- Depends on: A1, A4.
- Integration points: **IP-A10** — feeds B10's dashboard (`cloud_cost_usd` column).

#### A11. `Snapshot` / `Restore` — IaC-state implementation — `orchestrator/internal/orchestrator/server.go` (replace the `Unimplemented` stubs) — PLAN.md 179–182 — **M**
- `Snapshot`: for a T3 env, capture = `terraform state pull` (to the platform-managed
  backend, already there via A5, so this is mostly a metadata write) + `kubectl get -A`
  filtered to the learner's namespaces + a cloud resource inventory (Resource Explorer
  query) → a manifest in S3. **The compute (workspace pod + claimed account resources) is
  destroyed**; durable state is Git + TF remote state (§12.3 "suspension is the norm").
- `Restore`: re-provision the workspace pod, re-claim an account (or the same one if still
  pooled), `terraform apply` from the persisted state, re-attach.
- For T1/T2, `Snapshot`/`Restore` can stay a lighter workspace-volume snapshot (Phase 1's
  `M1` note mentions workspace snapshots) — scope decision D-P3-2 below.
- Deliverable: a T3 project attempt suspended at milestone 3, resumed a day later, with
  Terraform state intact and the repo unchanged; `Restore` recreates the infra with
  `terraform apply` reporting no changes.
- Depends on: A5, A6.
- Integration points: **IP-A11 (critical, joint design first)** — the `SnapshotRequest` /
  `SnapshotResponse` / `RestoreRequest` payload shape in `contracts/orchestrator.proto` is
  currently thin. Dev A + Dev B agree the manifest shape (what's in it, where it lives, how
  `Restore` addresses it) **before either side builds**. Dev B's Attempt Service calls
  these RPCs from the milestone state machine (B2).

### Track B — Project mode, evaluation, analytics (Dev B) — NOT gated on §2

#### B1. Schema — `project_milestone_state`, `project_submission` — `practice-core` migration (`attempt` schema) — line 1615 — **S**
- `project_milestone_state(attempt_id, milestone_key, status, submitted_at, score)`,
  `project_submission(id, attempt_id, milestone_key, repo_ref, commit_sha, submitted_at)`.
- Deliverable: migrations + repository classes + integration tests against real Postgres.
- Depends on: nothing.

#### B2. Project-mode milestone state machine — `practice-core/src/modules/attempt/` (extend, or a new `project` module) — PLAN.md 169, §12.3 — **L**
- A per-attempt milestone sequence `design → infra → implementation → hardening → final`;
  each milestone has `status ∈ {LOCKED, OPEN, SUBMITTED, GATED_PASS, GATED_FAIL}`.
- Gate logic: on `:submit`, run that milestone's validator + rubric slice (B3); a
  hard-gate milestone (design: "level 3/5 on architecture rubric", §12.3) blocks the next
  milestone until passed.
- Environment lifecycle per §12.3: milestone 1 needs **no environment**; milestones 2–4
  provision T3 on demand and destroy on idle (calls A6 `Provision` / A11 `Snapshot`);
  milestone 5 runs the full acceptance suite + inventory snapshot.
- New API surface (already sketched in `memory.md` 1531–1534):
  `POST /v1/practice/attempts/{id}/milestones/{key}:submit`,
  `GET /v1/practice/attempts/{id}/milestones`,
  `POST /v1/practice/attempts/{id}/defence/messages`.
- Deliverable: state-machine unit tests for every transition + gate; an integration test
  driving an attempt through all five milestones against fakes for A6/A11/the validators.
- Depends on: B1, B3, B5, B6.
- Integration points: **IP-B2** — calls A6 (`Provision` T3) and A11 (`Snapshot`/`Restore`)
  via the Orchestrator contract; builds against a fake until A6/A11 land.

#### B3. Six T3 validator executors — client-side dispatch + result recording — `practice-core/src/modules/evaluation/` — §6.2, §12.3 — **L**
- `IAC_STATE` — no drift (`terraform plan` exit code), remote state present, no secrets in
  state (scan the pulled state). Runs via the orchestrator `ExecShell`/`ExecValidator` in
  the T3 workspace with brokered read creds.
- `CLOUD_ASSERT` — assert cloud topology (VPC layout, no public data stores, encryption at
  rest, least-privilege IAM) via read-only AWS API calls with a **validator-scoped** STS
  role (extend `MintValidatorCredentials` for cloud — IP-B3).
- `TEST_SUITE` — run the repo's test command, parse JUnit/TAP output.
- `STATIC_ANALYSIS` — tfsec / checkov / trivy with authored thresholds.
- `CHAOS_PROBE` — kill a pod / drain a node in the deployed system, assert the service
  survives (HTTP probe stays green).
- `PERF_BENCH` — drive load (k6), assert p95 under target.
- Each maps to a typed executor with a JSON result; `ERROR` (validator itself broke) is
  **never scored against the learner** (doc §6.2 — the existing `validator-runner.service`
  already enforces this for Phase 1/2 types; extend the same dispatch).
- Deliverable: one executor at a time, each with a fake-orchestrator unit test + a
  real-run integration test against a throwaway T3 env. `activity_spec.schema.json` gains
  the per-type config shape (contract change, joint PR).
- Depends on: A6 (needs a real T3 env to run in); the orchestrator-side execution for
  `CHAOS_PROBE` (needs a node-drain capability) is **IP-B3b** — Dev A adds a
  `ExecChaosAction` RPC or reuses `InjectFault`'s mechanism (decision D-P3-3).
- Note: **`CLOUD_ASSERT` / `IAC_STATE` / `STATIC_ANALYSIS` can be built and tested against a
  static Terraform repo + a pre-made sandbox account before A6's full driver exists** —
  partial de-risking while §2's gate is being cleared.

#### B4. `sp.project.default` scoring profile — `practice-core/src/modules/evaluation/scoring-profile.ts` — PLAN.md 174 — **S**
- Signals → criteria → profile for project mode: milestone scores weighted
  (design + hardening + defence carry the rubric-heavy weight; infra + implementation carry
  the deterministic-validator weight), AI capped at 40 % of total (R5). Mastery updated
  across all mapped skills weighted by milestone scores (§12.3 POST).
- Deliverable: profile config + unit tests on the roll-up math + a golden-case test.
- Depends on: B2, B6.

#### B5. `rub.architecture.v3` — architecture rubric — `practice-core/src/modules/evaluation/` — §12.3 — **M**
- An engineered (not casually-prompted) rubric grader for the milestone-1 design doc:
  "appropriateness given the stated constraints, not the textbook answer". Reuses the
  Phase-2 AI-grader plumbing (`claude-ai-grader.service.ts` already exists) with a new
  rubric definition + calibration set.
- Deliverable: rubric definition, a hand-built calibration set (budget SME time — §13.1
  warns 3–4 weeks for the *first* rubric; this is the second, so lighter but non-trivial),
  kappa check against SME scores, injection-defence test (learner design text is
  delimited/quoted — rule 35, already the pattern in `ai-grader.interface.ts:63`).
- Depends on: the Phase-2 AI-grader (exists).

#### B6. Defence viva — question generator + transcript scorer — `practice-core/src/modules/evaluation/` — PLAN.md 173, §12.3 — **L**
- **Generator**: input = the learner's own milestone-1 design doc + their own commit
  history (from B9's Git hosting); output = 6–8 questions that probe divergence between
  stated design and actual implementation ("your doc says X for Y, your code uses Z — walk
  me through that"), plus reasoning probes ("what happens to in-flight requests during your
  deploy?"). Runs at grading time; calls the model directly (not Phase 4's Mentor Service).
- **Scorer**: `rub.reasoning.v1` over the viva transcript; human-reviewed for
  certification (§12.3, R13).
- API: `POST /v1/practice/attempts/{id}/defence/messages` (turn-by-turn).
- Deliverable: generator produces grounded questions (every question cites a specific
  section or commit); scorer + calibration set + human-review queue entry; adversarial
  test (learner can't prompt-inject the transcript to inflate the score).
- Depends on: B1, B5 (shares grader plumbing), B9 (commit history source).
- Integration points: emits `DEFENCE_MESSAGE` events (taxonomy, joint PR) for analytics.

#### B7. `activity_spec.schema.json` — project + milestone sections — `contracts/` — §12.3 — **S**
- New spec sections: `mode: PROJECT`, a `milestones[]` array (key, gate rule, validator
  refs, rubric ref, environment-required flag), `cloud.sku_exceptions`, per-validator
  config for the six new types.
- Joint PR (both devs approve — freeze rule). K10's `ActivitySpec` TS mirror updated in
  lockstep.
- Depends on: nothing; **blocks** B2, B3.

#### B8. ClickHouse analytics pipeline — `practice-core/src/modules/analytics/` (new) + `infra/clickhouse/` — §8.4, §13.3, line 2221 — **L**
- Stand up a ClickHouse cluster (A provisions per §3; Dev B owns ingestion + queries).
- A NATS/Kafka consumer that ingests the canonical `attempt_events` stream into ClickHouse
  (fan-out, no point-to-point — line 1463). Rollups attempt → activity → course → tenant →
  global, hourly (line 1851).
- Migrate `admin/analytics.service.ts` off the Postgres read-replica to ClickHouse for the
  aggregate queries; keep Postgres as the OLTP source of truth (never aggregate OLTP live —
  line 2239).
- Deliverable: events land in ClickHouse within seconds; the existing admin analytics
  endpoints return identical numbers from ClickHouse; a load test confirms it holds past
  ~10M events/day (the Postgres break point — line 2221).
- Depends on: the ClickHouse cluster (A-provisioned).
- Integration points: consumes the same event stream everything else does; no new contract
  beyond the deployment.

#### B9. Per-learner platform Git hosting — `infra/git-hosting/` (A) + `practice-core/src/modules/project/git.service.ts` (B) — PLAN.md 171, §12.3 — **M**
- **Decision D-P3-1**: self-hosted Forgejo/Gitea (one deployment, per-learner orgs/repos)
  vs. a thin wrapper over a hosted provider. Recommend **self-hosted Forgejo** — the sim
  fixture already uses Gitea so the ops knowledge exists; full control over retention
  (repos retained permanently — portfolio value) and over the commit-history read the viva
  generator needs; no per-seat cost at cohort scale.
- On project enrol (B2 week 0): provision the learner's repo, seed it with the requirements
  pack, hand the learner push access (over brokered short-lived creds or an SSH key scoped
  to that repo).
- `git.service.ts`: read commit history + diffs for B6; record `project_submission.repo_ref`
  + `commit_sha` per milestone.
- Deliverable: enrol → repo exists + seeded; learner pushes from the T3 workspace; viva
  generator reads the real commit graph; repo survives the post-project nuke.
- Depends on: the Git hosting deployment (A-provisioned).

#### B10. Admin cost dashboard — `practice-core/src/modules/admin/` + `web/` — §10.3, §11.3 — **M**
- API: cost per learner / course / activity / tenant, from `usage_meter` (compute, Phase 1)
  + `usage_meter.cloud_cost_usd` (A10) + `cloud_account` lifecycle (A4). Rollups from
  ClickHouse (B8).
- `web/` dashboard: cost-per-attempt distribution, cloud vs compute split, per-account
  spend with quarantine flags, budget-breach history.
- Deliverable: dashboard shows a finished T3 project's true cost (~$4.50 target, line 1827)
  broken down by milestone; a quarantined account is visible with its runbook link.
- Depends on: A4, A10, B8.
- Integration points: **IP-B10** — consumes `cloud_account` + `cloud_cost_usd` (A owns
  emission, schema frozen in the joint PR — same pattern as Phase 1's cost integration).

---

## 5. Execution order

### Sub-phase 3.0 — Gate + contracts + de-risking (parallel; ~3–4 wk)

| Dev A | Dev B |
|---|---|
| G1–G5 (orphan gate — §2) | B7 (spec schema — project + milestone + validator config) |
| A1 (AWS Org) + kick off the account-quota support ticket **day 1** | B1 (project schema + repos) |
| A2 (SCPs) + red-team verify | B3 partial — `CLOUD_ASSERT` / `IAC_STATE` / `STATIC_ANALYSIS` against a static repo + one hand-made sandbox account |
| Joint: **IP-A11** — agree the `Snapshot`/`Restore` payload shape and land the `contracts/orchestrator.proto` change | Joint: same |
| Joint: event-taxonomy additions (`MILESTONE_*`, `ACCOUNT_*`, `DEFENCE_MESSAGE`) — one `contracts/events.*` PR | Joint: same |

**Exit 3.0:** orphan gate signed off; `contracts/` frozen for Phase 3; Dev B can build the
milestone machine against fakes; three validator executors work against a static account.

### Sub-phase 3.1 — Account lifecycle + project skeleton (~5–6 wk)

| Dev A | Dev B |
|---|---|
| A5 (baseline TF) → A8 (nuke + verify) → A4 (Pool Manager) | B2 (milestone state machine, against fake orchestrator) |
| A3 (OIDC + STS broker) | B5 (`rub.architecture.v3` + calibration set — SME time starts here) |
| A7 (budget chain) | B9 (Git hosting — against the A-provisioned deployment once up) |

**Exit 3.1:** an account can be claimed → baseline-applied → nuked → verified → returned;
STS broker hands working creds and stops on suspend; Dev B's milestone machine runs
end-to-end against fakes; architecture rubric passes kappa.

### Sub-phase 3.2 — T3 driver + full evaluation (~5–6 wk)

| Dev A | Dev B |
|---|---|
| A6 (T3 driver) + A9 (OpenVSCode) — **swap Dev B's fake for the real driver here** | B3 remainder — `TEST_SUITE` / `CHAOS_PROBE` / `PERF_BENCH` against real T3 envs |
| A11 (`Snapshot`/`Restore` IaC-state) | B6 (viva generator + scorer) |
| A10 (Cost Explorer poll) | B4 (`sp.project.default`) |
| IP-B3b: chaos-action execution path (`ExecChaosAction` or `InjectFault` reuse) | B10 partial (dashboard API against A4/A10 data) |

**Exit 3.2:** a full project attempt runs through all five milestones on real T3, suspends
and resumes with TF state intact, produces a scored viva transcript, and rolls up into
`sp.project.default`.

### Sub-phase 3.3 — Analytics + dashboard + hardening (~3–4 wk)

| Dev A | Dev B |
|---|---|
| Nightly sweeper hardening; quarantine runbook; capacity/quota headroom review; launch-cap tuning | B8 (ClickHouse pipeline + analytics migration) |
| Load-soak the T3 teardown path (extends G4 to T3 scale) | B10 finish (full dashboard, quarantine visibility, budget-breach history) |
| Joint: chaos-day — kill accounts mid-nuke, mid-provision; confirm zero orphans + correct quarantine | Joint: same |

**Exit 3.3 = Phase 3 done:** see §8.

**Total after the gate: ~16–20 weeks ≈ 4 months** (matches the doc's estimate). The gate
(§2) is 2–3 weeks on top, and the AWS account-quota ticket must be filed on day 1 regardless.

---

## 6. Dependency graph (critical path)

```
AWS payer acct + quota ticket ──(days–weeks lead)──┐
                                                    v
A1 Org ──> A2 SCPs ──> A5 baseline TF ──> A8 nuke+verify ──> A4 Pool Manager ──┐
             │                │                                                 │
             │                └──> A3 OIDC+STS broker ───────────────────────┐  │
             │                                                              v  v
             └──> (SKU exception tag) ────────────────────────> A6 T3 driver ───> A11 Snapshot/Restore
                                                A9 OpenVSCode ──┘        │
                                                A7 budget chain ────────┤
                                                A10 Cost Explorer ──────┘
                                                                        │
B7 spec schema ──> B2 milestone machine ──> B3 validator executors <────┘ (needs real T3)
     │                   │                        │
     │                   ├──> B4 sp.project.default
     │                   └──> B6 viva  <── B9 Git hosting  <── (A-provisioned Forgejo)
     └──> B1 project schema
B8 ClickHouse <── (A-provisioned CH cluster)
B10 cost dashboard <── A4 + A10 + B8

HARD GATE (§2): G1–G5 orphan soak  ── blocks ──> A5/A6/A11 (any T3 compute)
                                    does NOT block ──> A1/A2, B1/B2/B7, B3-partial
```

Longest chain: quota-ticket → A1 → A2 → A5 → A8 → A4 → A6 → A11 → B3 → B6 → 3.3.

---

## 7. Cost & infrastructure estimate

### 7.1 One-time / setup

| Item | Estimate | Notes |
|---|---|---|
| AWS account-quota raise | $0 (support ticket), **days–weeks lead time** | Must be filed day 1. Target: peak-T3-concurrency × 2 accounts. |
| SME time — `rub.architecture.v3` calibration set | ~2 wk SME | Second rubric; §13.1 budgeted 3–4 wk for the first. |
| SME time — `rub.reasoning.v1` (viva) calibration | ~1–2 wk SME | |
| Multi-node cluster for the §2 orphan gate + T3 load-soak | see 7.2 (transient) | Can be torn down between runs. |

### 7.2 Recurring platform infra (monthly, steady-state; excludes learner sandbox spend)

Basis: `memory.md` §11.3 assumes ~10k monthly actives, peak ~60 concurrent T3, T3 project
~$4.50 (12 h suspended-adjusted), T3 sim ~$0.90.

| Component | Monthly | Basis |
|---|---|---|
| ClickHouse cluster | **$300–800** | 3 nodes (or ClickHouse Cloud dev tier ~$300; self-hosted on 3× `m6i.large` ~$250 + EBS). Event-analytics workload, cheap at this volume (line 2221). |
| Per-learner Git hosting (self-hosted Forgejo) | **$80–200** | 1–2× `m6i.large` + EBS for repo storage (repos retained permanently — storage grows ~linearly with completed projects; budget ~50 GB/1k projects). |
| OpenVSCode capacity on the practice cluster | **$400–900** | ~500 MB RAM/env × peak ~60 T3 + headroom ≈ 1–2 extra `m6i.2xlarge` nodes. Only during T3 sessions (aggressive teardown). |
| STS broker sidecars | negligible | Runs in the T3 pod already counted above. |
| Cost Explorer API + CUR-in-S3 | **$10–40** | CE API ~$0.01/request, hourly poll per active account; CUR storage trivial. |
| GuardDuty + Config + CloudTrail (org-wide, Platform acct) | **$150–500** | Scales with sandbox account count + event volume; GuardDuty ~$1–4/account/mo + data. |
| Nuke runner + nightly sweeper (Fargate/cron) | **$20–60** | Short tasks, per account per cleanup. |
| Terraform remote-state backend (S3 + DynamoDB, Platform acct) | **$5–20** | |
| **Platform infra subtotal** | **≈ $1,000–3,000 / mo** | Independent of learner sandbox spend. |

### 7.3 Learner sandbox spend (pass-through, variable — the real cost risk, R1)

| Driver | Estimate | Control |
|---|---|---|
| T3 sim attempt | ~$0.90 each (line 1826) | Per-account budget, 1 h TTL, aggressive teardown |
| T3 project (2 wk, suspended between) | ~$4.50 each (line 1827) | Suspension is the norm; compute lives hours not weeks |
| Account pool warm headroom | pool = peak × 2 (line 1880) → ~120 accounts | Warm accounts hold **no resources** when `AVAILABLE` → near-zero idle cost; cost is per-claim only |
| Tail-risk blowout (orphaned NAT gateway, forgotten RDS) | **the reason §2's gate exists** | Hard per-account budget (100 % ⇒ auto-revoke + terminate), independent hourly CE poll, nightly sweeper, mandatory post-nuke verification, launch cap on concurrent T3 |

**Do not launch T3 to learners without:** the hard per-account budget kill (A7), the
independent alarm (A10), the nightly sweeper (A8), and a launch cap on concurrent T3
attempts (A7) — all four are named in `memory.md` 2192 as the R1 mitigation set.

### 7.4 Operational commitment (not a dollar cost, but real)

- Account-pool capacity planning **days ahead of every cohort intake** (§5.3 trade-off,
  line 1002–1004) — account creation takes minutes and quota raises take weeks.
- A human on the quarantine queue — a `QUARANTINED` account means nuke + verify disagreed;
  someone investigates before it re-enters the pool.
- Kernel/patch SLA unchanged from earlier phases; SCP policy review on every new blueprint
  that requests an SKU exception.

---

## 8. Phase 3 Definition of Done

From `memory.md` §13.1 + §12.3 + the integration-point list:

1. A learner completes a full 5-milestone project on real AWS T3, pushing to a
   platform-hosted repo, and receives a scored defence viva transcript
   (human-reviewed sample).
2. `Snapshot`/`Restore` exercised for real: a project suspended at a milestone and resumed
   days later with Terraform remote state and repo intact; `terraform apply` on resume
   reports no changes.
3. All six validator types (`IAC_STATE`, `CLOUD_ASSERT`, `TEST_SUITE`, `STATIC_ANALYSIS`,
   `CHAOS_PROBE`, `PERF_BENCH`) execute against a real T3 env and record typed results;
   `ERROR` never scores against the learner.
4. Account lifecycle proven: claim → baseline → use → revoke → nuke → **verify** → return,
   with a deliberately-planted nuke-blind resource caught by verification and routed to
   `QUARANTINED` (not back to the pool).
5. Budget enforcement proven: a synthetic 100 % budget breach revokes credentials and
   force-terminates within one EventBridge cycle; the launch cap returns `ResourceExhausted`
   above the configured concurrency.
6. **Zero orphan environments AND zero orphan cloud resources** sustained through a T3-scale
   load-soak + 24 h after (the §2 gate, now re-proven at T3 — "the same bug that leaks a pod
   leaks a NAT gateway").
7. ClickHouse is the analytics store: admin analytics endpoints return from ClickHouse,
   verified identical to the Postgres numbers, holding past ~10M events/day in a load test.
8. Admin cost dashboard shows a finished T3 project's true blended cost (compute + cloud)
   broken down by milestone, ~$4.50 target, with quarantined accounts and budget-breach
   history visible.
9. OpenVSCode is the T3 editor (Monaco unchanged for T1/T2); no routable address into any
   environment.
10. `go build ./... && go vet ./... && go test ./...` clean in `orchestrator/`;
    `npm test` clean in `practice-core/` + `web/`; compose-backed integration suite green;
    no stub/fake/mock on any T3 critical path; reviewer sign-off recorded.

---

## 9. Open decisions (resolve before or during Sub-phase 3.0)

| ID | Decision | Recommendation |
|---|---|---|
| **D-P3-1** | Per-learner Git hosting: self-hosted Forgejo/Gitea vs. hosted-provider wrapper | **Self-hosted Forgejo** — ops knowledge already exists (sim fixture uses Gitea), full retention control, no per-seat cost, direct commit-history access for the viva generator. |
| **D-P3-2** | `Snapshot`/`Restore` for T1/T2 — implement now (workspace-volume snapshot) or leave `Unimplemented` and only implement the T3 IaC-state path | **T3 IaC-state path only** for Phase 3. T1/T2 volume snapshots were a Phase 1 `M1` line but no attempt state today requires them; deferring keeps A11 scoped. Revisit if a T2 sim needs mid-attempt suspend. |
| **D-P3-3** | `CHAOS_PROBE` execution — new `ExecChaosAction` RPC vs. reuse `InjectFault`'s mechanism | **Reuse `InjectFault`** where the action is pod-kill/node-drain (the fault-injection layer already does exactly this); add a thin `ExecChaosAction` only if a chaos action needs to target the learner's *deployed* system in the sandbox account rather than the platform cluster. |
| **D-P3-4** | Viva model call — direct vs. via a minimal gateway now (pre-Phase-4) | **Direct**, same as the Phase-2 `rub.incident-note.v2` grader. Do not pull Phase 4's LLM Gateway forward; the viva generator is a grading-time content generator, not the Mentor Service. |
| **D-P3-5** | Which region(s) for the first T3 launch | Pick the two SCP-allowed regions to match where the practice cluster and the majority of learners are; keep it to two (§5.3 "deny all regions except the two allowed"). |
| **D-P3-6** | ClickHouse: self-hosted vs. ClickHouse Cloud | Start **ClickHouse Cloud dev tier** for Phase 3 (lower ops load while the pipeline is new); migrate to self-hosted in Phase 5 if volume/cost warrants (R: multi-region sharding is Phase 5 anyway). |