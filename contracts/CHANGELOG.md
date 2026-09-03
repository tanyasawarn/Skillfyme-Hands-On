# Contract changelog

Every change to a file under `contracts/` gets one entry here, in the **same PR** that makes
the change, classified per the architecture doc's §3.6 version semantics:

| Kind | Meaning | Compatibility |
|---|---|---|
| **PATCH** | comment/doc only, or a new field that no code reads yet | wire-compatible both directions |
| **MINOR** | new field / new RPC, additively | old clients keep working; new field defaults to zero on old senders |
| **MAJOR** | field renumber or type change, RPC removal, semantic change to an existing field | **breaking** — requires the §3.6 canary rollout and coordinated deploy |

`buf breaking` (CI, see `../.github/workflows/ci.yml` → `contracts` job) rejects any MAJOR
change to `orchestrator.proto` automatically. A MAJOR therefore also needs a deliberate
`buf.yaml` exception or a new package version, plus sign-off from both `contracts/` code
owners (`../.github/CODEOWNERS`).

Format: newest first. `orchestrator.pb.go` is regenerated (`cd contracts && buf generate`) in
the same commit as any `orchestrator.proto` change — CI enforces this with
`git diff --exit-code`.

---

## 2026-08-27 — Phase 3 (0.7): Snapshot/Restore payload shape for T3 IaC-state

**MINOR** — `orchestrator.proto`. Purely additive: one new message, two new fields, doc
comments. `buf breaking` (vs `origin/main`) passes. `buf lint` passes. Go stubs regenerated
(`cd contracts && buf generate`) in the same change — `orchestrator/pkg/pb/orchestrator.pb.go`
+ `orchestrator_grpc.pb.go`. `cd orchestrator && go build ./... && go vet ./... && go test ./...`
all clean; `gofmt` clean.

- `SnapshotManifest` (new message) — the T3 IaC-state capture: `tf_backend_uri` +
  `tf_state_serial` + `tf_workspace` (pins which Terraform state a Restore applies from — the
  state itself stays in the platform-managed backend, not copied), `k8s_inventory_uri`
  (filtered `kubectl get -A` blob), `cloud_inventory_uri` + `cloud_resource_count` (Resource
  Explorer / Config inventory, tag-scoped to `attempt_id` — the reference the milestone-5
  "inventory survives the nuke" step and post-nuke verification both read), `sandbox_account_id`,
  `captured_at`.
- `SnapshotResponse.manifest` (field 4, `SnapshotManifest`) — populated only for T3; absent
  for T1/T2 workspace-volume snapshots, which stay fully described by the existing flat
  `snapshot_id` / `storage_uri` / `bytes` fields.
- `RestoreRequest.cloud_account_hint` (field 7, string) — the sandbox account id from the
  manifest, so the Account Pool Manager can try to re-claim the same account on resume.
  Ignored for T1/T2.

No behavioural change yet: `Snapshot` / `Restore` still return `Unimplemented` in
`internal/orchestrator/server.go` (that's Stage 3.3). This change only widens the contract so
3.3 and Dev B's Stage 1.6 milestone state machine can build against the agreed shape.
practice-core consumes the proto via `@grpc/proto-loader` at runtime (no generated artifact) —
it does not call these RPCs yet; the updated proto still parses (grpc-client tests 12/12,
full unit suite 170/170).

---

## 2026-08-27 — Phase 3 (0.8): Project + Cloud-account event taxonomy

**MINOR** — `events.md` + five new `events/*.schema.json`. Additive: new event types, new
payload schemas. No existing type or payload changed. Adding a new event type is a
joint-approval contract change (events.md cross-track rule 4) — same rule as the proto.

New taxonomy entries (events.md):

- **Project** — `MILESTONE_GATED` (`milestone_gated.schema.json`), `DEFENCE_MESSAGE`
  (`defence_message.schema.json`). Practice Core producers. `MILESTONE_SUBMITTED` was already
  in the table (reserved since K8); its Notes column is now filled in.
- **Cloud account** — `ACCOUNT_CLAIMED` (`account_claimed.schema.json`), `ACCOUNT_NUKED`
  (`account_nuked.schema.json`), `ACCOUNT_QUARANTINED` (`account_quarantined.schema.json`).
  Orchestrator (Account Pool Manager) producers, Phase 3 Stage 2.4. These still carry the
  envelope's required `attempt_id` — one vended account maps 1:1 to one attempt for its
  `IN_USE` lifetime; sweeper-time quarantine uses the last holding attempt + `reason:"sweeper"`.

TS taxonomy `practice-core/src/modules/event-store/attempt-event-type.ts` (`ATTEMPT_EVENT_TYPES`)
updated in lockstep — the six types added under new `// Project` / `// Cloud account` group
comments. `ReplayService`'s switch already no-ops unbranched types via its `default:` case, so
no change there. `npx tsc --noEmit` clean; new `attempt-event-type.spec.ts` (3 tests) asserts
the entries exist, are unique, and each documented payload schema file is present and
well-formed. Full practice-core unit suite 170/170. `orchestrator/` `go build ./...` clean
(no Go-side event-type enum; Go producers build payloads ad-hoc).

No proto change.

---

## 2026-08-27 — Phase 3 (0.9): PROJECT mode + milestones + T3 validator config

**MINOR** — `activity_spec.schema.json`. All additions are new, optional properties; no
existing field changed, no `required` array altered. Existing GUIDED_LAB / PRODUCTION_SIM
content validates unchanged (confirmed: `activity-spec.spec.ts` + `spec-lint.service.spec.ts`,
10 tests; full practice-core unit suite 167/167).

- `environment.cloud` — `{ regions?, sku_exceptions? }`. CLOUD_ACCOUNT-tier only. Feeds the
  SCP region allow-list and the expensive-SKU exception tag (PLAN_PHASE3_PROJECTS.md A2).
- `milestones[]` — the PROJECT-mode ordered gate sequence
  (`design`|`infra`|`implementation`|`hardening`|`final`), each with `gate`
  (`ALL_VALIDATORS_PASS`|`RUBRIC_MIN_LEVEL`|`BOTH`), `blocking`, `environment_required`,
  `task_keys[]`, and optional `rubric` + `min_level`. PLAN.md 169, memory.md §12.3.
- `defence` — `{ rubric, num_questions?, human_review? }`. The milestone-5 viva config.
- `tasks[].validators[].config` — per-type execution config for the six T3 validator types
  (`iac_state`, `cloud_assert`, `test_suite`, `static_analysis`, `chaos_probe`, `perf_bench`).
  Which sub-object applies is keyed off the sibling `type`. All optional; an executor returns
  ERROR (never a learner-scored FAIL) if the config it needs is absent. memory.md §6.2, §12.3.

TS mirror `practice-core/src/modules/catalog/activity-spec.ts` updated in the same change
(`ActivitySpecMilestone`, `ActivitySpecDefence`, `ActivitySpecEnvironmentCloud`,
`ActivitySpecValidatorConfig` + the six per-type interfaces). `npx tsc --noEmit` clean.

No proto change, so no `buf generate` / stub regeneration involved.

---

## Reconcile — 2026-08-27 (baseline audit of the three consumers)

First changelog entry doubles as a one-time reconciliation of `orchestrator.proto` against its
three consumers, because the "frozen after Week 1, both-owner PR" rule in `PLAN.md` lapsed and
changes landed undocumented. State as found:

1. **`orchestrator.proto` ↔ `orchestrator/pkg/pb/*.pb.go` (generated Go) — WAS STALE, now fixed.**
   The proto declared `ProvisionRequest.health_gate_json` and `attempt_id` on seven request
   messages; the committed `orchestrator.pb.go` predated all of them (no `HealthGateJson`
   field, no `GetAttemptId()` getters). Regenerated with `buf generate` (pinned plugins, see
   `buf.gen.yaml`); `cd orchestrator && go build ./... && go vet ./...` clean on the result.
   Going forward the CI stub-freshness step (`buf generate` + `git diff --exit-code`) prevents
   this recurring.

2. **`orchestrator.proto` ↔ practice-core dynamic loader — CONSISTENT.**
   practice-core loads `orchestrator.proto` at runtime via `@grpc/proto-loader`
   (`src/common/base-grpc-client.ts`, `src/modules/evaluation/grpc-validator-executor.ts`,
   `scripts/content-ci.ts`) — no generated artifact, so nothing to drift. Audited every field
   the TS side sends or reads (`attemptId`, `blueprintId`, `blueprintVersion`, `tier`,
   `fixtures`, `ttlMinutes`, `idleTimeoutMinutes`, `networkPolicy`, `idempotencyKey`,
   `healthGateJson`, `environmentId`, `reason`, `faultId`, `faultVersion`, `params`,
   `validatorId`, `validatorType`, `run`, `expectJson`, `timeoutMs`, `command`, `snapshotKey`,
   `scopes`, `ttlSeconds`): every one exists in the current proto.

3. **`orchestrator.proto` ↔ `contracts/events/*.schema.json` — CONSISTENT (separate surface).**
   The event schemas describe NATS payloads, not gRPC messages; they are not generated from
   the proto and are not expected to match field-for-field. One intentional superset:
   `validator_result.schema.json` allows `status: "SKIP"` while `ExecValidatorResponse.status`
   documents only `PASS | FAIL | ERROR` — `SKIP` is a scoring-engine outcome (a validator that
   was not run), never an executor return value. No conflict; recorded so it is not
   re-flagged.

---

## Post-freeze additive changes (backfilled 2026-08-27)

These landed in the working tree after the Phase-0 freeze without a changelog entry at the
time. All are **additive** — `buf breaking` against the initial commit confirms zero breaking
changes. Backfilled here so the record is complete from this point on.

### MINOR — `attempt_id` added to seven request messages (RPC ownership checks)

| Message | Field | Purpose |
|---|---|---|
| `ConnectRequest` | `attempt_id = 2` | handler verifies it matches `env.environment.attempt_id` before minting a session token; `PermissionDenied` on mismatch |
| `SnapshotRequest` | `attempt_id = 2` | same ownership check |
| `DestroyRequest` | `attempt_id = 3` | same ownership check |
| `MintCredentialsRequest` | `attempt_id = 4` | same ownership check |
| `ExecShellRequest` | `attempt_id = 4` | same ownership check |
| `InjectFaultRequest` | `attempt_id = 5` | same ownership check (first of the set — the others followed this precedent) |
| `ExecValidatorRequest` | `attempt_id = 7` | same ownership check |

Rationale and implementation record: `../PLAN_RPC_AUTHZ.md`. Server enforcement in
`orchestrator/internal/orchestrator/server.go` (`checkEnvironmentOwnership`), with unit +
live-integration tests. `Provision` deliberately has no `attempt_id` ownership check — it
*creates* the environment, so there is nothing yet to check against.

Compatibility: a caller that omits `attempt_id` sends the empty string, which the server
rejects with `PermissionDenied` — so this is MINOR on the wire but a **required** field in
practice. Every practice-core call site was updated in the same body of work.

### MINOR — `ProvisionRequest.health_gate_json = 11`

JSON-encoded `health_gate` array from the activity spec (`activity_spec.schema.json` §3.2 /
doc §7.3): checks that must pass, in order, before the environment is considered READY (or,
for `PRODUCTION_SIM`, before any fault is applied). Wire type is a plain string carrying a
richer shape parsed by the receiver — same precedent as `InjectFaultRequest.params`, chosen
over a typed message per check kind.

Compatibility: empty string means "no richer health gate declared" — the state every
`GUIDED_LAB` activity is in today (pod-Ready remains the whole gate for those, exactly as
before the field existed). Old senders that omit it are unaffected.

Implementation: `orchestrator/internal/validation/health_gate.go` (`RunHealthGate`,
`ParseHealthGateJSON`); practice-core reads the spec's top-level `health_gate` and sends it
JSON-encoded from `attempt.service`.
