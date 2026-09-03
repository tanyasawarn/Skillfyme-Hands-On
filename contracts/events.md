# Attempt Events Contract

Source of truth: doc §4.2 (event model), §4.1 (attempt lifecycle), PLAN.md integration points #2b, #4.

This is the append-only vocabulary the whole platform speaks. `attempt_events` (owned by Dev B, `attempt`
schema) is the sink; Dev A's Session Broker and Environment Orchestrator are producers over NATS. JSON Schemas
for each payload live alongside this file in `contracts/events/*.schema.json`.

## Transport

- Bus: NATS JetStream (per doc §13.2 recommendation — Kafka only if volume demands it later).
- Subject naming: `env.telemetry.<event_type>` for Dev A → Dev B execution/environment events (agreed in
  PLAN.md #2b). Everything else (validation, assistance, scenario, lifecycle originating in Practice Core)
  publishes on `attempt.<event_type>`.
- Every message carries `attempt_id` as a required field — the universal correlation key (doc §13.5 #1).
- Producers own their event's schema; consumers must not assume fields beyond what's documented here without a
  contract change (same joint-approval rule as `contracts/orchestrator.proto`).

## Envelope

Every event, regardless of producer, is shaped as the `attempt_events` row from doc §4.2:

```
{
  "attempt_id":   "uuid",
  "seq":          "monotonic per attempt (assigned by the event-store writer, not the producer)",
  "occurred_at":  "RFC3339 timestamp, set by the producer",
  "actor":        "LEARNER | SYSTEM | VALIDATOR | AI | ADMIN",
  "type":         "one of the taxonomy below",
  "payload":      { "...": "type-specific, see contracts/events/<type>.schema.json" }
}
```

`seq` is assigned at write time by Dev B's event-store writer — producers never set it themselves, since two
producers (Session Broker, Orchestrator) can race and only the writer can guarantee monotonicity per attempt.

## Taxonomy

| Category | Type | Producer | Notes |
|---|---|---|---|
| Lifecycle | `ATTEMPT_CREATED` | Practice Core | |
| Lifecycle | `ENV_REQUESTED` | Practice Core | fired on Provision RPC call |
| Lifecycle | `ENV_READY` | Orchestrator | |
| Lifecycle | `ENV_FAILED` | Orchestrator | |
| Lifecycle | `ATTEMPT_STARTED` | Practice Core | on first learner interaction, not on READY (§4.1 step 23) |
| Lifecycle | `SUSPENDED` | Practice Core | |
| Lifecycle | `RESUMED` | Practice Core | |
| Lifecycle | `SUBMITTED` | Practice Core | |
| Lifecycle | `EVALUATED` | Evaluation Service | |
| Lifecycle | `SEALED` | Practice Core | |
| Execution | `COMMAND_EXECUTED` | Orchestrator (Session Broker tap) | `{cmd, exit_code, duration_ms, cmd_hash, source, cwd?}` — **integration point #2b**. Captured via a PROMPT_COMMAND shell hook (real cmd text + exit code, not output-hash guessing); `cwd` not yet emitted (Phase 1 gap, see `contracts/events/command_executed.schema.json`) |
| Execution | `FILE_CHANGED` | Orchestrator | `{path, op, diff_ref}` |
| Execution | `EDITOR_SAVE` | Orchestrator / Web (T0/T1 Monaco file API) | |
| Execution | `TERMINAL_SESSION_OPEN` / `TERMINAL_SESSION_CLOSE` | Orchestrator | |
| Validation | `VALIDATION_REQUESTED` | Practice Core | |
| Validation | `VALIDATOR_RESULT` | Evaluation Service | `{validator_id, pass, detail}` |
| Validation | `TASK_PASSED` / `TASK_FAILED` / `TASK_SKIPPED` | Practice Core | derived from validator results |
| Assistance | `HINT_REQUESTED` | Practice Core | `{task, level}` |
| Assistance | `AI_MESSAGE` | AI Gateway | `{role, tokens, policy_decision}` — Phase 4 |
| Assistance | `SOLUTION_VIEWED` | Practice Core | |
| Environment | `RESET` | Practice Core → Orchestrator | |
| Environment | `FAULT_INJECTED` | Orchestrator | `{fault_id}` — Phase 2 |
| Environment | `IDLE_DETECTED` | Orchestrator | |
| Environment | `TTL_WARNING` | Orchestrator | |
| Environment | `ENV_DESTROYED` | Orchestrator | **integration point #4** — Practice Core consumes this to transition attempt state, no direct DB writes across the boundary |
| Environment | `SNAPSHOT_TAKEN` | Orchestrator | |
| Scenario | `TICKET_OPENED` | Practice Core | Phase 2 |
| Scenario | `ESCALATION_FIRED` | Practice Core | Phase 2 |
| Project | `MILESTONE_SUBMITTED` | Practice Core | Phase 3 — `{milestone_key}`. Fired when a learner submits a project milestone for gating. |
| Project | `MILESTONE_GATED` | Practice Core | Phase 3 — `{milestone_key, outcome, score?, rubric_level?}`. The gate decision (`GATED_PASS` / `GATED_FAIL`) after validators + rubric run. See `contracts/events/milestone_gated.schema.json`. |
| Project | `DEFENCE_MESSAGE` | Practice Core | Phase 3 — `{role, turn, question_ref?}`. One turn of the milestone-5 viva transcript (`role` = `EXAMINER` \| `LEARNER`). Scored later against `rub.reasoning.v1`. See `contracts/events/defence_message.schema.json`. |
| Cloud account | `ACCOUNT_CLAIMED` | Orchestrator (Account Pool Manager) | Phase 3 — `{cloud_account_id, region}`. A vended sandbox account moved `AVAILABLE → IN_USE` for this attempt. See `contracts/events/account_claimed.schema.json`. |
| Cloud account | `ACCOUNT_NUKED` | Orchestrator (Account Pool Manager) | Phase 3 — `{cloud_account_id, verified, resources_remaining}`. `aws-nuke` ran and the mandatory verification pass completed. `verified: true` ⇒ the account returned to `AVAILABLE`. See `contracts/events/account_nuked.schema.json`. |
| Cloud account | `ACCOUNT_QUARANTINED` | Orchestrator (Account Pool Manager) | Phase 3 — `{cloud_account_id, reason, resources_remaining?}`. Post-nuke verification found leftover resources (or nuke itself failed); the account is held for human review, never returned to the pool. Pages on-call. See `contracts/events/account_quarantined.schema.json`. |

### A note on the `attempt_id` requirement for cloud-account events

`ACCOUNT_CLAIMED` / `ACCOUNT_NUKED` / `ACCOUNT_QUARANTINED` still carry `attempt_id` as the
envelope's required correlation key — one vended account maps to exactly one attempt for its
whole `IN_USE` lifetime (memory.md §10.3: "one account = one attempt makes attribution
exact"). `ACCOUNT_QUARANTINED` that happens during the *nightly sweeper* (no active attempt)
uses the last attempt that held the account as `attempt_id`, plus `reason: "sweeper"`.

## Cross-track rules (do not violate)

1. Dev A never writes directly to `attempt_events` or any Dev B-owned table. All state changes flow as events.
2. Dev B never calls into the execution fabric (K8s API, cloud APIs) directly — only through
   `orchestrator.proto`.
3. `ENV_DESTROYED` is the only way an attempt learns its environment is gone, whether that's a clean submit
   teardown, idle/TTL/budget teardown, or the reaper force-destroying past a deadline. Practice Core's Attempt
   Service must handle this idempotently (see PLAN.md integration point #4).
4. Adding a new event type is a contract change — same joint-PR-approval rule as the proto file.
