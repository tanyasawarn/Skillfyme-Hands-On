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
