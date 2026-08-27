# PLAN.md Phase 3 — Infrastructure Boilerplate Consolidation

Tracks the 12 real items from `PLAN.md`'s "Phase 3 — Infrastructure boilerplate consolidation"
section (lines 312-315), scoped against actual verified code state (background audit,
2026-08-23: 0 done, 0 partial, 12 not done — every duplication PLAN.md describes is still
present verbatim).

**Status as of 2026-08-23: all 12/12 items complete** (U7-U12, S3, S4, S8, K8, K9, K10), each
with the shared abstraction built, real call sites migrated, functional tests, and — for S8/K9
specifically — dedicated security verification against the real running stack.

Each item requires: the shared abstraction built, existing call sites migrated to use it
(not a big-bang swap — one at a time per PLAN.md's own sequencing note), functional tests,
and security validation before being marked complete.

PLAN.md's own sequencing note (line 314): extract the shared abstraction first with existing
call sites still using their old inline code, add tests for the new abstraction in isolation,
*then* migrate call sites one at a time so each migration is independently revertable.

PLAN.md flags **S8 and K9 as security-adjacent — get a second reviewer** (line 315). Both will
get explicit security-focused verification given that framing.

---

## Orchestrator (Go)

- [x] ~~**U7** — `getOrNotFound[T]()`: generic function in `internal/faultinjection/
      faultinjection.go`, migrated the 9 real matching call sites (out of the "12+"
      originally estimated) across `handlers.go` (5) and `handlers_batch2.go` (4). The
      remaining occurrences flagged by the original grep were investigated individually, not
      assumed to match: 2 sites (`handlers_batch4.go`'s egress-proxy fault,
      `handlers_batch2.go`'s networkpolicy-overblocks-traffic fault) use INVERTED NotFound
      logic where "not found" is the fault's own intended success state, not a target-to-break
      failure -- genuinely different control flow `getOrNotFound` doesn't fit, correctly left
      untouched rather than forced into a helper that would silently change their behavior.
      Takes `(resourceKind, errLabel, name)` as three separate strings rather than deriving
      errLabel from resourceKind -- checked the real original 9 messages first and found
      irregular abbreviations a mechanical lowercase transform would have gotten wrong
      ("PersistentVolumeClaim" -> "pvc", "ResourceQuota" -> "resourcequota", not
      "resourceQuota"), so preserving exact original wording needed an explicit parameter, not
      a computed one. 5 new unit tests written BEFORE migrating (fake clientset for the success
      path, a real `apierrors.NewNotFound` for the NotFound path including the Node
      cluster-scope wording, a plain error for the wrap-other-errors path, an explicit
      errLabel-not-derived-from-resourceKind regression test). `go build`/`vet`/`gofmt`/`test`
      all clean, zero regressions (51 total faultinjection tests passing, 46 pre-existing + 5
      new). Live-verified against the real orchestrator: a fault targeting a nonexistent Node
      returns the exact original NotFound message (cluster-scope wording correct); a fault
      targeting a nonexistent ConfigMap returns the exact original namespace-scope wording; a
      fault targeting a real, existing Node succeeds end-to-end (taint applied, confirmed via
      `kubectl describe node`, immediately cleaned up).
      **Separately discovered, real, pre-existing bug (not caused by this change, not part of
      the 12-item list, flagged for visibility)**: live-testing this item surfaced that
      `internal/warmpool.Manager` has no method removing an environment from its Redis pool
      index when that environment is destroyed by any path other than a successful `Claim()` --
      an environment I destroyed during U9's live verification was still returned by a later
      `Claim()` call (confirmed via the orchestrator log: `warm-pool hit: env=...` for a
      namespace `kubectl get ns` already reported `NotFound` for), causing that `Provision` call
      to hang indefinitely against a nonexistent namespace. `Claim()` itself uses `SPop` (atomic
      pop), so the stale entry does NOT keep recurring on every future claim -- this was a
      one-time ghost entry, not a permanently-broken pool -- but the underlying gap (no
      `warmpool.Manager` integration in the `Destroy` teardown path) is real and would recur for
      any other warm-pooled environment destroyed by the reaper, a budget hard-stop, or a manual
      `Destroy` call before it's ever claimed. Out of scope for U7 itself; noted here rather than
      silently worked around~~
- [x] ~~**U8** — `requireEnvironment(ctx, envID)`: extracted as a method on `*Server`
      (`internal/orchestrator/server.go`), migrated 6 call sites (`Connect`,
      `MintValidatorCredentials`, `InjectFault`, `CaptureBaseline`, `ExecValidator`,
      `ExecShell`). `Destroy` deliberately kept its own inline check -- its `!exists` case
      returns `AlreadyDestroyed: true`, not a `NotFound` error, a genuinely different contract
      the helper doesn't (and shouldn't) cover. Not unit-testable in isolation: `Provisioner`'s
      `clientset` field is the concrete `*kubernetes.Clientset` type, not an interface, so no
      fake-clientset seam exists for `Server`-level methods (confirmed: `server_test.go`'s own
      doc comment already states the rest of `Server` has no test infrastructure for this
      reason, only pulled-out pure functions do). Verified instead via `go build`/`vet`/`gofmt`/
      `test` (all clean, zero regressions) plus live grpcurl against the real orchestrator +
      k3s: a nonexistent-environment call to a migrated RPC (`ExecShell`) still returns
      `NotFound` with the identical message; `Destroy` against a nonexistent environment still
      correctly returns `{alreadyDestroyed: true}`, confirming its distinct contract survived
      the extraction; the happy path (real provisioned environment, `ExecShell` succeeds)
      verified end-to-end~~
- [x] ~~**U9** — `RunTicker(stop, interval, fn, runImmediately)`: new `internal/loop` package
      (matches PLAN.md's own suggested package name). Investigated the flagged "inconsistency"
      before treating it as a bug: warmpool's immediate-fire is a real functional requirement
      (a `Claim()` call must find a pre-provisioned environment ready, not an empty pool for a
      full interval after startup), not a bug the other three are missing -- kept as an
      explicit `runImmediately` parameter rather than unifying to one behavior. Takes a plain
      `<-chan struct{}` rather than `context.Context` directly so `costmeter.Meter` (whose
      lifecycle is `NewMeter()...Close()`, not request-scoped, no `context.Context` in its
      constructor) could adopt this helper via its own `stopCh` unchanged, without a forced,
      out-of-scope constructor signature change. Migrated all 4 call sites (reaper, idledetect,
      costmeter, warmpool). 5 new unit tests written BEFORE migrating (per PLAN.md's
      sequencing), covering: fires on every tick, `runImmediately=true` fires before the first
      tick, `runImmediately=false` correctly waits, stops promptly on the stop channel closing,
      never fires again after stop -- all passing under `-race`, and re-run 10x uncached with
      no flakiness. `go build`/`vet`/`gofmt`/`test` all clean across every touched package
      (reaper, idledetect, costmeter, warmpool, loop), zero regressions -- costmeter's own 15
      pre-existing tests (including the budget-threshold tests from earlier this session) still
      pass unchanged. Live-verified the actual behavioral distinction this item was about:
      restarted the orchestrator with `WARM_POOL_FILL_INTERVAL=5m` and warm pool enabled --
      confirmed via log timestamps that the pool filled a real environment within the same
      second the process started (not after waiting 5 minutes), and the resulting pod was
      confirmed `Running` via `kubectl`~~
- [x] ~~**U10** — `ignoreAlreadyExists(err)`: extracted as a pure `func(error) error` in
      `internal/k8s/provision.go`, migrated all 8 call sites (`createNamespace`,
      `applyResourceQuota`, `applyLimitRange`, `applyDefaultDenyNetworkPolicy`, the
      egress-allowlist NetworkPolicy, ServiceAccount creation, Pod creation, Service creation).
      4 new unit tests written BEFORE migrating call sites (per PLAN.md's own sequencing) --
      this caught a real bug during the migration itself: a `replace_all` edit accidentally
      matched the helper's own function body too, rewriting it into infinite self-recursion
      (`return ignoreAlreadyExists(err)` calling itself). Caught by re-reading the diff before
      running anything, not by the tests catching a stack overflow -- but confirmed after the
      fact that running `go test` first (this file's actual sequencing) would have caught it
      too, since the recursion would have stack-overflowed the test process rather than
      silently passed. Fixed immediately, full suite reverified clean afterward. `go build`/
      `vet`/`gofmt`/`test` all clean, zero regressions. Live-verified: a real `Provision` call
      against the live k3s cluster exercised all 8 migrated call sites in one real pipeline run
      (READY status), confirmed every resulting K8s object exists correctly (ResourceQuota,
      LimitRange, 2 NetworkPolicies, ServiceAccount, Pod all present and correct via `kubectl
      get`)~~
- [x] ~~**U11** — `patchFirstContainer(containerName, mutate)`: real struct marshaling
      (`encoding/json`) replacing all 4 hand-written `fmt.Sprintf` JSON patch strings (not 3 as
      first estimated -- found the 4th, a multi-line StatefulSet exec-command patch, by
      grepping more broadly). **A first design attempt reused `corev1.Container`/`corev1.Probe`
      directly as the mutation target -- this session's own tests, written before migrating any
      call site per PLAN.md's sequencing, caught it producing WRONG output**: `corev1.Probe`'s
      `TimeoutSeconds`/`InitialDelaySeconds`/`PeriodSeconds` are `int32,omitempty`, so an
      explicitly-set `0` marshals identically to never-touched -- silently dropping the
      `"initialDelaySeconds":0` every original hand-written patch relied on to FORCE-RESET that
      field under strategic-merge-patch semantics (omitted ≠ explicit-zero: omitted leaves an
      existing nonzero value on the live object untouched, explicit-zero overwrites it). Fixed
      by hand-declaring a real `containerPatch`/`containerPatchProbe` type (not reusing the K8s
      generated type) with `*int32` fields specifically so "explicitly zero" (non-nil pointer to
      0) and "not set" (nil pointer) are distinguishable -- exactly the distinction the original
      hand-written strings could express but Go's naive `omitempty` on `int32` cannot. Also
      preserved `errLabel`-style exact original wording (via explicit `resourceKind`/`errLabel`
      params, not derived) for consistency with U7's design choice.
      8 new unit tests, including two written specifically for the bug this session found:
      one asserting explicit-zero `initialDelaySeconds` IS present in the marshaled patch, one
      asserting a genuinely-unset field is genuinely absent (not JSON `null`, which under
      strategic-merge-patch means "delete this field," a third, different, equally-wrong
      outcome). Plus a dedicated injection-safety test (a value containing `", "otherField":
      {"escalate": true}, "x":"` must marshal as a harmless literal string, never structurally
      corrupt the patch). Deleted the now-fully-dead `strategicMergePatch` helper (zero
      remaining callers after all 4 migrations). Also updated a stale security doc-comment on
      `applyReadinessProbeTooAggressive` that described a class of risk (raw-JSON-number
      interpolation) this fix eliminates entirely, not just for that one field.
      `go build`/`vet`/`gofmt`/`test` all clean (59 total faultinjection tests, 51 pre-existing +
      8 new). Live-verified against the real orchestrator + k3s cluster across all 4 patch
      shapes using real Deployments/StatefulSets created for this test: memory-limit patch
      (`kubectl get -o jsonpath` confirmed `{"limits":{"memory":"32Mi"}}` exactly), image-tag
      patch (confirmed `nginx:nonexistent-tag-xyz`), StatefulSet exec-command probe (confirmed
      correct shell-escaped command array), and -- the most important live check, matching the
      exact bug this item's own tests caught -- set a real `initialDelaySeconds:30` on a live
      Deployment's probe by hand, re-applied the readiness-probe-too-aggressive fault, and
      confirmed the live object's `initialDelaySeconds` was genuinely force-reset (no longer
      `30`), not left stale, proving the fix holds against the real K8s API server, not just the
      unit test's JSON-structure comparison~~
- [x] ~~**U12** — `RestrictedPodSecurityContext()` / `RestrictedContainerSecurityContext
      (readOnlyRootFilesystem bool)`: new exported functions in `internal/k8s/provision.go`
      (exported since `faultinjection` needs to call them; already imports `internal/k8s`
      elsewhere, so no import-cycle risk). `RestrictedPodSecurityContext()` is identical in
      both real call sites (`createWorkspacePod`, the traffic-spike fault's Job) -- zero
      parameters needed. `RestrictedContainerSecurityContext` takes
      `readOnlyRootFilesystem bool` since the two call sites genuinely differ there: workspace
      pods need a writable root fs (learner's own writes, package-manager installs), the
      traffic-spike fault's containers have no such requirement (call site kept `false` to
      match its original, previously-unset field, documented as behaviorally identical since
      K8s's own default for an unset field is `false`). Every other container-level field
      (AllowPrivilegeEscalation=false, Capabilities.Drop=[ALL]) stays fixed, not parameterized,
      since neither real call site needs to vary it. 5 new unit tests written BEFORE migrating
      (per PLAN.md's sequencing), including a real regression class this codebase cares about
      given U9's shared-pointer close call: independent-pointer tests confirming two calls
      never share the same `*int64`/`*bool` address (so a future caller mutating "its own"
      SecurityContext can't corrupt another caller's). One real bug caught during the first
      migration edit (not a design bug, an editing mistake): a `replace_all`-adjacent partial
      edit to `createWorkspacePod` left a dangling extra `},` producing a genuine Go syntax
      error, caught immediately by `go build` (not silently shipped) and fixed before proceeding.
      `go build`/`vet`/`gofmt`/`test` all clean afterward, zero regressions across every touched
      package (`k8s`, `faultinjection`) -- specifically re-confirmed
      `TestApplyPodCrashloop_SatisfiesPodSecurityRestricted` and the 7 `TestApplyTrafficSpike*`
      tests (the exact tests that would catch a PSS regression) still pass. Live-verified
      against the real orchestrator + k3s cluster, both call sites: a fresh `Provision` reached
      `READY` (workspace pod `1/1 Running`) with `kubectl get -o jsonpath` confirming the exact
      pod-level and container-level security context values the shared builders produce; the
      traffic-spike fault's Job pod reached `2/2 Running` with the same confirmed values --
      this is the exact PodSecurity-restricted admission path that rejected an earlier,
      unrelated handler outright when it had no SecurityContext at all (a real bug found live
      earlier this session), so this was the one U-item where a live PSS-acceptance check
      mattered most, not just a unit-test pass~~

## practice-core (NestJS)

- [x] ~~**S3** — `BaseGrpcClient`: new abstract class in `practice-core/src/common/`, extended
      by both `GrpcOrchestratorClient` and `GrpcValidatorExecutor`. Caught and fixed a real path
      bug before it ever ran: the base class's `resolveContractsPath` initially used
      `../../../contracts` (one level too few, computed relative to `src/common/`'s own actual
      depth) instead of the correct `../../../../contracts` -- verified precisely via a real
      `path.resolve` + `fs.existsSync` check against the compiled `dist/` layout before trusting
      it, not by assumption. Preserved two REAL behavioral asymmetries between the two original
      subclasses rather than silently unifying them: only `GrpcOrchestratorClient` ever logged a
      warning when `ORCHESTRATOR_SHARED_SECRET` is unset (kept via an overridable
      `onSharedSecretResolved` hook, default no-op); the two subclasses' `call()` error handling
      genuinely differed (`GrpcOrchestratorClient` always wrapped into a real `Error`,
      `GrpcValidatorExecutor` rejected with the raw `grpc.ServiceError`) -- audited every
      `catch` block in `GrpcValidatorExecutor` before unifying and confirmed only `.message` was
      ever read off the caught value, never `.code` or any other `ServiceError`-specific field,
      so collapsing both to the always-wrap behavior changes nothing observable; fixed the two
      resulting `err as grpc.ServiceError` casts (now genuinely inaccurate types) to
      `err instanceof Error ? err.message : String(err)`, and removed the now-fully-unused
      `grpc` import from that file. `npx tsc --noEmit` clean, full unit suite (115 tests) and
      integration suite (72 tests) both pass unchanged. Live-verified against the real running
      practice-core + orchestrator: a real `submit` HTTP call exercised both migrated clients
      end-to-end against a genuinely stale/nonexistent environment reference -- confirmed via
      the DB's own event log (`VALIDATOR_RESULT` rows with `status: ERROR`, never `FAIL`,
      correctly matching doc §6.2's "an RPC-level failure must never be scored against the
      learner" contract) that `GrpcValidatorExecutor.execute()` made a real `ExecValidator` RPC
      through the migrated `BaseGrpcClient.call()`, received a real `NotFound` error (correctly
      produced by U8's `requireEnvironment` on the orchestrator side -- the same live call
      exercised both S3 and U8 together), and the new `err instanceof Error` handling correctly
      caught and recorded it~~
- [x] ~~**S4** — `NatsSubscriberBase<TEnvelope>`: new abstract class in
      `practice-core/src/common/`, extended by both `CommandExecutedConsumer` and
      `EnvDestroyedConsumer`. Template-method shape: the base owns connect/subscribe/consume-
      loop/drain/close and the decode+parse+dispatch+catch wrapper; each subclass supplies its
      `subject`, envelope type (generic param), and two hooks. Preserved a real behavioral
      difference deliberately rather than unifying it: the two consumers' "is this envelope
      malformed, drop it" check genuinely differs (`EnvDestroyedConsumer` only requires
      `attempt_id`; `CommandExecutedConsumer` also requires `payload`) -- kept as an abstract
      `isValidEnvelope()` hook (which also owns its own subclass-specific warning log message,
      matching each original's distinct wording) rather than a fixed check in the base, since
      unifying either direction would either silently drop previously-valid messages or stop
      catching a previously-caught malformed case. `handleCommand` (the actual business logic in
      `CommandExecutedConsumer`) is completely untouched -- confirmed the existing
      `command-executed-consumer.integration.spec.ts` (6 tests, accesses the private
      `handleCommand` method directly via a type-cast, matching this codebase's own established
      pattern for testing NATS-adjacent logic without a real connection) still passes unchanged.
      Full unit suite (115) and integration suite (72) both pass. Live-verified against the
      real running practice-core + NATS: published a real message directly to
      `env.telemetry.COMMAND_EXECUTED` via the `nats` CLI and confirmed it was genuinely
      consumed and written to `attempt_events` by the migrated consumer; published a malformed
      message (empty `attempt_id`) and confirmed via the DB that it was correctly dropped
      BEFORE any write was attempted (the empty string would have failed the `uuid` column
      type at the DB layer, proving the drop happened at `isValidEnvelope`, not later); then
      published a second valid message immediately after and confirmed it was still processed
      correctly -- proving the consumer loop survived the malformed message exactly as the
      "must not crash the loop" contract requires, live, not just in a unit test~~
- [x] ~~**S8** — `AttemptOwnershipGuard` — **security-flagged, PLAN.md's own second-reviewer
      note**: real `CanActivate` guard (`practice-core/src/modules/attempt/
      attempt-ownership.guard.ts`) applied via `@UseGuards(AttemptOwnershipGuard)` on all 13
      original call sites (`provision`, `start`, `submit`, `connect`, `getById`,
      `getEvaluation`, `getTasks`, `previewHint`, `revealHint`, `listFiles`, `readFile`,
      `writeFile`, `submitArtifact`), replacing the private `assertOwnedByCaller` method
      entirely (fully removed, not left as dead code). Registered as a provider in
      `attempt.module.ts` (needed for DI to construct it with `AttemptRepository` injected).
      Fails closed on a non-string route param (Express types `req.params` values as
      `string | string[]`) rather than passing an array through to the DB layer -- a real
      TypeScript compile error the migration itself surfaced, not a speculative hardening.
      The actual ownership decision (`isOwnedByCaller`) is split into its own zero-dependency
      module (`attempt-ownership.ts`) for the same reason `cooldown.ts`/`criteria.ts` already
      are: this project's Jest config can't load any file that transitively imports
      `AttemptRepository` -> `kysely` (confirmed live: a first test file importing the guard
      directly failed with a real `SyntaxError: Unexpected token 'export'` from kysely's ESM
      build) -- keeping the security-critical comparison itself dependency-free makes it
      directly unit-testable, arguably a better outcome than mocking a repository would have
      been. 8 new unit tests on the pure function, explicitly covering: correct-owner-allowed,
      wrong-user-same-tenant-denied, right-user-wrong-tenant-denied (cross-tenant isolation),
      both-wrong-denied, nonexistent-attempt-denied, missing-caller-denied, and a
      case-sensitivity check. Full unit suite (123 tests) and integration suite (72 tests) both
      pass. **Live-verified the actual security property against the real running server**:
      minted a genuinely-valid JWT (real signature, correct secret) for a different `userId`
      than the demo user who owns a real attempt, then confirmed via real HTTP requests that
      this attacker token is correctly rejected with `403 Forbidden` / "attempt does not belong
      to caller" on 3 separate routes (`GET :id`, hint-reveal, file-read) -- while the genuine
      owner's own token against the same attempt correctly succeeds (`200 OK`) -- this is the
      actual doc §9.1 cross-learner-data-access attack live-demonstrated as blocked, not just
      asserted in a mock~~
- [x] ~~**K8** — `AttemptEventType` const/union: new `practice-core/src/modules/event-store/
      attempt-event-type.ts` — `ATTEMPT_EVENT_TYPES` const array (29 entries, mirrors
      `contracts/events.md`'s Taxonomy table exactly, same categories/order as that doc's
      section headers) + derived `AttemptEventType` union type. Event-type strings were
      previously untyped (`string`) on both `AppendEventInput.type` and `AttemptEventRow.type`
      in `event-store.repository.ts`, with zero shared taxonomy across the 6 real files that
      write business events -- confirmed live before the fix that a typo like `'ATTEMPT_CREATD'`
      compiled and would have silently written/read the wrong event bucket with no error
      anywhere.

      **Two design attempts tried and rejected before the final design, both confirmed by real
      compiler behavior, not assumption:**
      1. A generic type parameter *with a default* on `AppendEventInput`/`AttemptEventRow`
         (`type: T = AttemptEventType`), on the theory that callers not specifying `T` get the
         narrower default. Verified via a deliberate typo positive-control that this does
         **not** work: TypeScript infers `T` from the argument's own literal type whenever
         inference succeeds, and the default only applies when inference is impossible -- so a
         call site with `type: 'ATTEMPT_CREATD'` compiled with zero error, confirmed with a
         minimal reproduction (`function f<T = Foo>(x: {val: T}): T` accepts `f({val: 'ZZZ'})`
         clean). The default never actually constrained a real caller.
      2. A *constrained* generic (`T extends AttemptEventType = AttemptEventType`) does
         correctly reject the typo, but then also rejects `event-store.repository.ts`'s own
         generic-infrastructure-layer integration tests, which deliberately exercise
         seq/ordering/advisory-lock mechanics with arbitrary non-taxonomy strings (`'A'`, `'B'`,
         `` `EVT_${n}` ``) decoupled from any real business event -- `string` is not a subtype
         of the union, so `<string>` instantiation is a real compile error ("Type 'string' does
         not satisfy the constraint"). The repository's own tests need exactly this generality;
         real business callers need exactly the opposite. One shared generic parameter on one
         interface cannot satisfy both.

      **Final design**: `EventStoreRepository`/`AppendEventInput`/`AttemptEventRow` reverted to
      fully non-generic (`type: string`, matching the original shape) -- this repository stays
      an infrastructure-layer, taxonomy-agnostic append-only log, which is also true to doc
      §4.2's own framing ("the single most valuable observability decision... build this on day
      one," not "stores only these 29 specific types"). Real compile-time safety instead comes
      from a new, non-generic `TypedAppendEventInput` interface (`type: AttemptEventType`
      directly, no generic, no default) plus a standalone wrapper function
      `appendTypedEvent(events: EventStoreRepository, input: TypedAppendEventInput):
      Promise<{seq: string}>` that just calls `events.append(input)`. Real business-event call
      sites use `appendTypedEvent` instead of calling `.append()` directly; the repository's own
      tests keep calling `.append()` directly and are untouched. Re-verified the typo case is a
      real compile error (with a "Did you mean" suggestion) and the valid case compiles clean
      against this final shape via the same scratch-file methodology, then deleted the scratch
      file.

      **Migration**: all 6 real files with business-event `.append()` call sites migrated
      mechanically (`this.events.append(` → `appendTypedEvent(this.events, `, plus the import) —
      `attempt.service.ts` (7 sites: `ATTEMPT_CREATED`, `ENV_REQUESTED`, `ENV_READY`,
      `ENV_FAILED`, `ATTEMPT_STARTED`, `SUBMITTED`, `SEALED`-adjacent), `hint.service.ts` (1,
      `HINT_REQUESTED`), `command-executed.consumer.ts` (1, `COMMAND_EXECUTED`),
      `evaluation/artifact.service.ts` (2, `EDITOR_SAVE`/`AI_MESSAGE`),
      `evaluation/evaluation.service.ts` (1, `EVALUATED`),
      `evaluation/validator-runner.service.ts` (3, `VALIDATION_REQUESTED`, `VALIDATOR_RESULT`,
      and a ternary `taskPassed ? 'TASK_PASSED' : 'TASK_FAILED'` -- confirmed both ternary
      branches type-check and execute correctly against `TypedAppendEventInput`, not just the
      literal-string sites). `eventHasBeenRecorded`'s `type` parameter in `attempt.service.ts`
      narrowed from `string` to `AttemptEventType`. `event-store.integration.spec.ts`'s own
      ordering/concurrency tests deliberately left calling `.append()` directly with
      non-taxonomy strings, matching the final non-generic repository design.

      Full suite: `npx tsc --noEmit` clean; unit suite 123/123 passing (unchanged count --
      business logic untouched, only wrapper call sites changed); integration suite 72/72
      passing against real Postgres. **Live-verified against the real running stack** (not just
      compiled code -- confirmed the `nest start --watch` process had actually rebuilt and
      restarted with the migrated code before testing): started a real in-progress attempt
      (`ATTEMPT_STARTED`, actor `LEARNER`) via `POST .../start`, requested a real hint
      (`HINT_REQUESTED`) via `POST .../tasks/t1/hints`, published a real NATS message on
      `env.telemetry.COMMAND_EXECUTED` (`COMMAND_EXECUTED`, actor `LEARNER`) and confirmed the
      consumer wrote it, then submitted the attempt via `POST .../submit` and confirmed the full
      resulting chain (`SUBMITTED` → `VALIDATION_REQUESTED` → `VALIDATOR_RESULT` ×4 →
      `TASK_FAILED` ×2, from the ternary site → `EVALUATED`) — all read back with correct
      `seq`/`type`/`actor`/`payload` directly from `attempt.attempt_events` via `psql` against
      the real database. `artifact.service.ts`'s two sites (`EDITOR_SAVE`/`AI_MESSAGE`) weren't
      separately live-fired (the test attempt was already terminal by the time artifact
      submission was reached, and re-running eligibility gates to get a fresh attempt was
      out of scope for this check) but are covered by the clean `tsc --noEmit` and the identical,
      already-proven-live `appendTypedEvent` pattern used at every other site.~~
- [x] ~~**K9** — `Role` enum — **security-flagged, PLAN.md's own second-reviewer note**: new
      `practice-core/src/modules/auth/role.ts` — `Role` enum (`LEARNER`/`AUTHOR`/`ADMIN`, the
      only 3 values that exist anywhere in this codebase, confirmed via a full grep before
      choosing the list) plus `isRole(value: string): value is Role`, a real runtime type guard,
      not just a compile-time type. `@Roles()` (`roles.decorator.ts`) previously accepted any
      bare `string` with zero compile-time checking; confirmed live before the fix that
      `@Roles('admni')` (a typo) compiled clean and made the guarded route unreachable by every
      real role -- fails closed for that specific bug (denies everyone) but is exactly the
      pattern that becomes fail-open the moment someone "fixes" the symptom by loosening the
      check instead of fixing the typo.

      **The harder half of this item, and the reason a runtime guard (not just a type) was
      required**: `AuthClaims.role` narrowed from `string` to `Role`, but the value populating
      it (`auth.guard.ts`) originates from a decoded JWT payload -- attacker-controlled input
      that has crossed a real trust boundary. `jwt.verify<T>()`'s generic parameter is a
      TypeScript cast, not a runtime check; narrowing `AuthClaims.role`'s *type* to `Role` with
      no accompanying runtime validation would have made `req.auth.role: Role` a claim the
      compiler enforced everywhere downstream while being trivially false for any JWT
      containing an arbitrary role string (e.g. a token minted before this fix, or a
      hand-crafted one) -- strictly worse than the untyped original, since call sites would stop
      defending against a case the type system now claims can't happen. Fixed by calling
      `isRole()` on the raw decoded string in `AuthGuard.canActivate()` before ever constructing
      `req.auth`, rejecting with `401 'token has an unrecognized role'` on a miss -- this is the
      actual point where `Role` becomes a true statement about the value, not just a type
      annotation over it.

      Second, closely related finding: `AuthController.devLogin()` (`POST /v1/auth/dev-login`)
      accepted a fully unvalidated caller-supplied `role` string in the request body and minted
      a real, validly-signed JWT carrying it verbatim -- confirmed live before the fix that
      `{"role": "superadmin"}` returned `200` with a working token. Fixed with the same
      `isRole()` guard, now `400 Bad Request` on an unrecognized role. `scripts/mint-dev-token.ts`
      (a standalone CLI, not part of the Nest app / Jest-covered surface) got the identical
      `isRole()` check on its CLI arg -- a typo'd role arg now exits 1 with a clear message
      instead of silently minting a garbage-role token.

      Migrated: `roles.decorator.ts` (`Roles(...roles: Role[])`), `roles.guard.ts`
      (`getAllAndOverride<Role[]>`), `auth.types.ts` (`AuthClaims.role: Role`, with a doc comment
      explaining why this type is only true because of `auth.guard.ts`'s runtime check, not
      because of the cast), `auth.guard.ts` (the `isRole()` enforcement point), `auth.controller.ts`
      (dev-login validation), `admin.controller.ts` (3 real `@Roles()` call sites: class-level
      `@Roles(Role.ADMIN, Role.AUTHOR)`, 2 method-level `@Roles(Role.ADMIN)` overrides on
      `review-decision`/`publish`), `scripts/mint-dev-token.ts`. Deliberately left untouched:
      `env-destroyed.consumer.ts`/`orchestrator-client.interface.ts`'s `'admin'` literal --
      that's an unrelated `reason` union for environment-destroy causes, not a user role, checked
      individually rather than assumed to match. The DB column (`user_account.role`, plain `text`
      with a `DEFAULT 'learner'`, no CHECK constraint) deliberately left as-is, matching K8's own
      precedent: the DB/infra layer stays loosely typed, the TypeScript layer is where K9's
      compile-time + runtime safety actually lives.

      8 new unit tests on `isRole`/`Role` (`role.spec.ts`): all 3 real roles accepted, a typo
      rejected, empty string rejected, wrong-casing rejected, `ROLE_VALUES` matches the exact
      3-value set, and a type-narrowing usage check. Full unit suite 131/131 passing (123
      previous + 8 new). Full integration suite 72/72 passing, unchanged (auth/role concerns
      aren't exercised by the Postgres integration layer). `npx tsc --noEmit` clean project-wide,
      including `scripts/mint-dev-token.ts` (no separate tsconfig scope for `scripts/`,
      confirmed by reading `tsconfig.json` -- no `include`/`exclude`, so the whole tree is one
      compile unit).

      **Live security verification against the real running server** (found and fixed a real
      environment hazard first: 4 separate stale `nest start --watch` processes from different
      prior-session days were all racing on the same `dist/`, so the server on :3001 was
      returning stale pre-K9 behavior even after a correct rebuild -- killed all 4 plus the
      stale child process, started exactly one fresh watcher, confirmed via `dist/` file mtimes
      and a clean boot log before trusting any test result against it):
      - `POST /v1/auth/dev-login {"role":"superadmin"}` -> real `400 Bad Request`
        `"unrecognized role: superadmin"` (previously silently minted a working token)
      - `POST /v1/auth/dev-login {"role":"learner"}` / `{"role":"admin"}` -> both real `201` with
        working tokens (legitimate roles unaffected)
      - `scripts/mint-dev-token.ts author` -> mints correctly; `scripts/mint-dev-token.ts
        suprAdmin` -> exits 1 with `"unrecognized role: suprAdmin (expected one of: learner,
        author, admin)"`, no token printed
      - A real learner token against `POST /v1/admin/activities/lint` (class-level
        `admin`-or-`author` gate) -> real `403 Forbidden` `"insufficient role for this route"`
      - A real author token against the same route -> real `201`, actual lint response returned
        (past the gate, executing real business logic) -- confirmed this is a genuine pass and
        not a masked failure by re-running with valid YAML and getting real schema-validation
        issues back, not an auth error
      - A real author token against `POST /v1/admin/activity-versions/:id/review-decision`
        (method-level `admin`-only override) -> real `403 Forbidden` -- the separation-of-duties
        gate (author can draft, only admin can approve) demonstrated as actually enforced, not
        just asserted
      - A real admin token against the same `review-decision` route -> real `409 Conflict`
        `"activity version ... not found"` -- past the role gate entirely, hit the real
        not-found business logic, confirming admin is correctly authorized where author is not~~
- [x] ~~**K10** — `ActivitySpec` TS interface: new `practice-core/src/modules/catalog/
      activity-spec.ts` — a full, field-for-field TypeScript mirror of `contracts/
      activity_spec.schema.json` (same required/optional split, confirmed by mechanically
      walking the JSON Schema's own `required` arrays via a small Python script rather than
      eyeballing it, so the type's required-ness genuinely matches the schema's, not just a
      plausible guess). Before this, `content.activity_version.spec_jsonb` (`Jsonb<unknown>` in
      db/schema.ts, correctly, since Kysely can't know a JSONB column's real shape) was
      independently re-cast to a different ad-hoc partial inline type at **8 real call sites**
      across 6 files -- `artifact.service.ts` (2 casts, plus its own `ArtifactSpecEntry`
      interface), `catalog.repository.ts` (`PublishActivityVersionInput.spec`),
      `evaluation.service.ts` (2 casts, one reusing the already-shared `TaskSpec`),
      `command-executed.consumer.ts` (its own `ProcessSignalsSpec` interface),
      `hint.service.ts` (its own `ActivityTaskSpec`/`ActivityHint` interfaces),
      `attempt.service.ts` (3 casts) -- each guessing at only the fields it happened to need,
      with zero shared source of truth, so a real field rename in the schema had no single place
      that would fail to compile. `admin.controller.ts`'s `lintAndParse` and both
      `scripts/publish-*.ts` CLI scripts had a 9th/10th/11th independent copy of the same
      partial shape, all migrated to the one real type too.

      **A real functional bug found and fixed during migration, not just a typing exercise**:
      `attempt.service.ts`'s `createAttempt()` read the activity's `mode` via
      `(version as { mode?: string }).mode`, but `version` comes from
      `selectFrom('content.activity_version')` -- `mode` lives on `content.activity`, not
      `content.activity_version` (confirmed against `db/schema.ts`'s own
      `ActivityTable`/`ActivityVersionTable` interfaces), so this cast was reading a field that
      can never exist there and the expression silently fell through to the `'GUIDED_LAB'`
      default on *every* attempt regardless of the activity's real mode. Confirmed live impact
      via a real query against the running Postgres: 1 real published activity
      (`sim.sre.checkout-latency-incident`) is `PRODUCTION_SIM`, meaning every attempt created
      against it before this fix was silently stored as `GUIDED_LAB` -- zero current behavioral
      impact only because nothing in this codebase currently branches on `attempt.mode` yet
      (confirmed via a full grep), but this would have silently broken the first
      `PRODUCTION_SIM`-specific gate anyone adds. Fixed by joining `content.activity` into the
      existing `createAttempt()` query (`select(['v.id','v.activity_id','v.spec_jsonb','a.mode'])`)
      and reading the real, joined `a.mode` directly -- no cast.

      **A second, real design finding, not a bug but a genuine boundary**: `TaskSpec`/
      `ValidatorSpec` (`validator-runner.service.ts`) were deliberately kept as their own
      narrower internal-execution-input types rather than folded into `ActivitySpecTask`/
      `ActivitySpecValidator` -- `ValidatorSpec.expect` is required (matches the schema, fixed a
      real drift in this type's first draft, which had wrongly made it optional) but
      `ValidatorSpec.timeoutMs` is camelCase runtime-execution config, distinct from
      `ActivitySpecValidator`'s snake_case wire field `timeout_ms` -- genuinely different shapes
      serving different layers (content-schema vs. execution-layer), confirmed by checking
      `ValidatorSpec`'s real definition field-by-field rather than assuming a shared name meant
      a shared shape.

      **A third finding, surfaced by the stricter type rather than fixed silently**:
      `PublishActivityVersionInput.spec` initially became the full non-partial `ActivitySpec`,
      which broke ~15 real call sites (2 CLI scripts, 4 integration test files) that legitimately
      publish deliberately minimal test-only specs -- `insertVersion()` itself only ever reads
      `id`/`version`/`meta.difficulty_level`/`meta.estimated_minutes`/`environment.blueprint`/
      `environment.cost_budget_usd`/`skills` at runtime, so a fully-required `ActivitySpec` was
      stricter than the code's own real contract. User-confirmed direction: kept one canonical
      type as the source of truth rather than reintroducing a duplicate ad-hoc shape --
      `PublishActivityVersionInput.spec` is now
      `Omit<Partial<ActivitySpec>, 'id'|'version'|'meta'|'environment'|'skills'> & {...}` with
      only those 5 runtime-read fields required (and `meta`/`environment` themselves narrowed to
      just their 2 runtime-read fields each, rest optional) -- every field that IS present on a
      real spec object is still checked against the real schema shape, but a minimal test
      fixture isn't forced to fabricate a schema-complete object it doesn't need. Fixing this
      surfaced further real gaps in existing test fixtures once actually checked against the
      schema: a `faults` entry missing required `apply_at`, a `tasks` entry missing required
      `title`, a `validators` entry missing required `on_fail` -- all genuine test-fixture gaps
      that happened to work before only because nothing checked them, now fixed with real values
      rather than suppressed.

      3 new tests (`activity-spec.spec.ts`): parses a real, real-content YAML file
      (`content/activities/lab.linux.navigate-filesystem.yaml`) through the actual
      Ajv-compiled schema validator (`SpecLintService`, the same one CI/the CMS use), asserts
      genuine `valid: true` with zero issues, then checks the parsed object satisfies
      `ActivitySpec`; a positive-control test deletes a required field and confirms `lint()`
      genuinely rejects it (proving the test isn't vacuously passing); a third constructs a
      fully-populated `PRODUCTION_SIM`-only object (`health_gate`/`faults`/`process_signals`/
      `artifacts_required` -- no real content file uses these sections yet) and confirms it
      type-checks and passes the real schema validator too.

      Full suite: `npx tsc --noEmit` clean project-wide (11 real call sites across
      src/ + scripts/ + test/ migrated, confirmed via a final grep that zero
      `spec_jsonb as {` ad-hoc casts and zero `ArtifactSpecEntry`/`ActivityTaskSpec`/
      `ProcessSignalsSpec` references remain anywhere). Unit suite 134/134 passing (131
      previous + 3 new). Integration suite 72/72 passing, including the corrected test
      fixtures and the `mode`-bug-fix path exercised end-to-end by
      `attempt-lifecycle.integration.spec.ts`. **Live-verified against the real running
      server**: the exact join query `createAttempt()` now runs
      (`content.activity_version v INNER JOIN content.activity a`) confirmed live against real
      Postgres to return `mode: PRODUCTION_SIM` for the one real `PRODUCTION_SIM` activity
      (`sim.sre.checkout-latency-incident`) -- an HTTP-level end-to-end attempt-creation check
      was attempted with a throwaway test user (to avoid touching the demo user's existing
      non-terminal attempts from earlier K8/K9 testing) but stopped short of a real mastery
      gate requiring seeded skill-mastery evidence, judged out of scope to fabricate; the DB
      query plus the passing integration suite (which exercises this exact code path against a
      clean DB per test) were treated as sufficient, and the throwaway test user was deleted
      after. Separately, `admin.controller.ts`'s migrated `lintAndParse` (now returning
      `ActivitySpec`) was live-verified via a real `POST /v1/admin/activities/lint` call with a
      real admin-role JWT against the real content YAML file used in the unit test above --
      returned genuine `valid: true` with the full parsed spec, confirming both K10's type
      migration and K9's `@Roles(Role.ADMIN, Role.AUTHOR)` gate work correctly together on the
      real endpoint.~~

---

## Verification gate (every item, before marking done)

- Functional: existing test suite still passes (orchestrator `go test ./...`; practice-core
  `npx jest` + integration suite) AND new tests cover the extracted abstraction in isolation
- Security: S8 and K9 specifically get a dedicated security-focused test pass (ownership
  bypass attempts, role-string-typo/invalid-role rejection) given PLAN.md's own flag
- Live verification where the change touches a runtime path already exercised live this
  session (orchestrator RPCs, practice-core HTTP endpoints)
