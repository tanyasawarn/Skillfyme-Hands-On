# PLAN.md Phase 1 & 2 — Trivial Extractions + Confirmed-Bug Fixes

Tracks the 20 real items from `PLAN.md`'s "Phase 1 — Trivial extractions" (line 299-303) and
"Phase 2 — Confirmed-bug fixes" (line 305-310) sections, scoped against actual verified code
state (audit run 2026-08-24: checked every named file/symbol directly via `ls`/`grep`, not
assumed from PLAN.md's own description).

**Status as of 2026-08-24 (before this pass): 1/20 done** (K5, and only as an accidental
side-effect of Phase 4's C1 work, not built as its own item), **19/20 not done**. Unlike Phase 3
and Phase 4 (both fully closed with dedicated tracking files), Phase 1 and Phase 2 were skipped
entirely in the original execution order — work jumped straight to the higher-risk Phase 3/4
items. Two of the four "confirmed live bugs" the whole PLAN.md audit was originally justified
around (K12, S7) are still live in production code today.

**Status as of 2026-08-24 (mid-pass): 14/20 done, 1 partial/unverified, 5 paused.**
Phase 2 (all 4 confirmed-bug fixes: K5/K7/K12/S7) fully closed. Phase 1's web items (10/10) and
practice-core's U6 fully closed. Orchestrator items **paused mid-work** after discovering a
second live session concurrently, heavily editing the same orchestrator and practice-core code
(70 files / ~5,000 lines changed beyond this session's own edits) — user-confirmed decision to
pause rather than risk corrupting that in-progress work.

**Status as of 2026-08-24 (resumed, final): 20/20 done.** Resumed after re-confirming the repo
had settled: `provision.go` (the file mid-edit at pause time) hadn't changed in ~44 minutes,
`go build`/`go vet`/`go test ./...` were all clean against the other session's current state
before touching anything further, and this session's own earlier work (S7/K7/K12/U6) was
independently re-verified intact and correctly built upon by the other session. Completed K13
(finished the 5 files left unmigrated at pause time, 1 site in `idledetect` deliberately left per
its own documented architectural boundary), U13, K16, K18, K19, K20, and **K17 — a real,
previously-unverified security fix**: a committed 64-char hex string was silently used as the
JWT-signing secret fallback whenever `WS_GATEWAY_JWT_SECRET` was unset, defeating
`wsgateway.NewTokenValidator`'s own empty-secret panic guard; changed to no-fallback, matching
practice-core's `AuthModule`'s existing fail-loud behavior on a missing secret. All 20 items now
closed: `go build`/`go vet ./...`/`gofmt -l .` clean, full orchestrator test suite passing (every
package, including 4 new leaf packages this pass added: `internal/ttl`, `internal/destroyreason`,
`internal/envstatus`, plus `internal/k8s`'s `ObjectMeta`/managed-namespace additions), full
practice-core unit suite (152/152) and web suite (95/95) still green, confirming zero regressions
introduced by any item in this file.

Each item requires: the extraction/fix itself, existing call sites migrated (not left
half-migrated), functional tests, and — per this session's established convention — live
verification against the real running stack where the change is observable end-to-end.

---

## Phase 2 — Confirmed-bug fixes (do first: these are real, live bugs)

PLAN.md's own gating note (line 307-309): two decisions need to be made before this phase starts.
1. K5/K12: which of the two disagreeing values is correct.
2. S7: confirm raising `ConflictException` on concurrent evaluation writes is acceptable.

- [x] ~~**K5** — attempt-status color/label unification — DONE, pre-existing (landed as a
      side-effect of Phase 4's C1 `Badge` component work: `web/src/lib/attempt-status.ts`
      exists, `ATTEMPT_STATUS_META` is the canonical map, all 4 call sites migrated). No
      further work needed.~~
- [x] ~~**K7** — `AttemptStatusGroups` (TERMINAL/RETRYABLE_FROM/SUCCESS), new
      `practice-core/src/common/attempt-status-groups.ts`, derived once and exported (not a
      private per-class array). Migrating this surfaced a SECOND real, previously-undocumented
      drift instance beyond the one PLAN.md originally flagged: `AttemptRepository.
      findMostRecentCompletedAttempt`'s own hand-copied status list was independently missing
      `PROVISION_FAILED` (silently different from `AttemptService`'s correct list, for no
      documented reason) — meaning an attempt that failed to provision was NOT treated as
      "completed" for retry-chaining purposes. Fixed by deriving both call sites
      (`AttemptService.handleEnvironmentDestroyed`'s `TERMINAL` check and
      `AttemptRepository.findMostRecentCompletedAttempt`'s `RETRYABLE_FROM` filter, the latter
      = TERMINAL minus PASSED) from the one shared construct. 7 new unit tests
      (`attempt-status-groups.spec.ts`) including one pinning `PROVISION_FAILED` presence in
      `RETRYABLE_FROM` specifically so this drift can't silently regress. 1 new integration test
      (`attempt-lifecycle.integration.spec.ts`: "chains a retry after a PROVISION_FAILED
      attempt") proving the fix against real Postgres — forces an attempt to `PROVISION_FAILED`
      via `attemptRepo.transition()`, then confirms a subsequent `createAttempt()` correctly
      chains `retry_of_attempt_id`/`retry_index`, which it did NOT before this fix. Full unit
      suite 152/152 passing, integration suite 78/78 passing, `tsc --noEmit` clean.~~
- [x] ~~**K12** — `EligibilityConstants.MAX_CONCURRENT_ENVIRONMENTS_PER_LEARNER`, new export in
      `practice-core/src/common/constants.ts` (alongside Phase 4's `MasteryConstants`/
      `GrpcClientConstants`/`TimeoutConstants` — the already-scaffolded home). Both the
      comparison (`>= 1`) and the user-facing message ("1 active environment per learner") in
      `eligibility.service.ts` now read from the one shared constant instead of two independent
      literals that were internally consistent today only by coincidence. 1 new unit test
      (`constants.spec.ts`) pinning the value. Full unit suite 152/152 passing, `tsc --noEmit`
      clean.~~
- [x] ~~**S7** — `EvaluationService` now calls `AttemptRepository.transition()` (injected via
      constructor) instead of a raw `updateTable('attempt.attempt').set(...).where('version','=',
      ...)` inline update that silently no-op'd on a version mismatch. Hit the same circular-
      module-import constraint Phase 4's S5 (`ActivitySpecReader`) hit — `AttemptModule` imports
      `EvaluationModule`, so `EvaluationModule` can't import `AttemptModule` back for its
      `AttemptRepository` export — resolved the same way: `AttemptRepository` registered
      directly as a provider in `EvaluationModule` (stateless, so a second DI instance is
      harmless). Updated all 5 test files that directly instantiate `EvaluationService`
      (`attempt-lifecycle`, `hint`, `evaluation`, `recommendation` integration specs) to pass the
      new constructor arg — all 5 already had an `attemptRepo` instance in scope. **User-
      confirmed direction** (per PLAN.md's own Phase 2 gating note): raising `ConflictException`
      on a concurrent evaluation write is the correct, intended behavior change. 1 new
      integration test (`evaluation.integration.spec.ts`: "evaluate() raises ConflictException on
      a concurrent version mismatch instead of silently no-oping") — genuinely races a concurrent
      `attemptRepo.transition()` against an in-flight `evaluate()` call via `Promise.allSettled`
      (a sequential before/after setup doesn't work: `evaluate()` re-reads the attempt row fresh
      at its own top, so a version bump before calling it just gets absorbed into that initial
      read rather than causing a genuine conflict — confirmed this by first writing a naive
      version of the test, watching it fail to reproduce the race, and fixing the test's own
      timing before trusting it). Confirms both the `ConflictException` throw AND that the
      concurrent writer's own write survives untouched (evaluate()'s failed write doesn't
      clobber it). Full unit suite 152/152 passing, integration suite 78/78 passing, `tsc
      --noEmit` clean.~~

## Phase 1 — Trivial extractions (mechanical, low-risk)

### Web (`web/src/lib/`, `web/src/app/globals.css`, `web/src/components/ui/`)

- [x] ~~**K1** — new `web/src/lib/config.ts`, single `API_BASE_URL` export. Migrated all 3 real
      independent readers of `NEXT_PUBLIC_API_BASE_URL` (`api-client.ts`, `auth-token.ts`, and
      `error-message.ts`'s own "Could not reach practice-core" fallback message, found while
      touching this — a 3rd site PLAN.md's original audit didn't separately flag). 1 new test
      module implicitly covered via existing `error-message.spec.ts`'s regex-only assertion
      (didn't need updating). `tsc --noEmit` clean, full web suite passing.~~
- [x] ~~**K2** — new `web/src/lib/routes.ts`: `attemptRoute(id)` / `catalogEntryRoute(id)`.
      Scoped to the 2 real parameterized routes (static routes like `/catalog`/`/skills` have
      nothing to parameterize and stay owned by `Sidebar.tsx`'s `NAV_ITEMS`). Migrated all 5 real
      call sites (`page.tsx` x2, `catalog/[activityVersionId]/page.tsx`,
      `history/page.tsx`, `catalog/page.tsx`) — the `catalog/page.tsx` site needed an added
      explicit null-guard (`entry.activity_version_id` is nullable; the original template-string
      form silently stringified `null` to `"null"`, masked by the surrounding `available` ternary
      — the function-call form correctly surfaces this as a real type error instead of an
      unnoticed edge case, fixed by guarding on the value directly). 2 new tests
      (`routes.spec.ts`).~~
- [x] ~~**K3** — new `web/src/lib/route-params.ts`: `COURSE_QUERY_PARAM = 'course'`. Migrated
      all 3 real sites (`catalog/page.tsx`, `skills/page.tsx`, `Sidebar.tsx`'s read + its 2 write
      sites building the URL). 1 new test.~~
- [x] ~~**K4** — new `web/src/lib/courses.ts`: `COURSES` array + `courseLabel(slug)`. Migrated
      all 3 real sites — `Sidebar.tsx`'s `COURSES` array (the richest of the 3 originals, now the
      canonical source), `catalog/page.tsx`'s `COURSE_TITLE`, `skills/page.tsx`'s `COURSE_TITLE`
      (both deleted, not left as dead code). 3 new tests.~~
- [x] ~~**K6** — new `web/src/lib/mastery.ts`: `masteryBandMeta/Variant/FillClassName(band)`.
      Confirmed the two existing maps (`page.tsx`'s `BAND_COLOR` raw-CSS-class map,
      `skills/page.tsx`'s `BAND_VARIANT` `Badge`-variant map) already agreed color-for-color
      before consolidating — this preserves current behavior exactly, not a new mapping decision.
      Migrated both real sites. 4 new tests.~~
- [x] ~~**T1** — spacing (`--space-1` through `--space-7`) and radius (`--radius-sm/md/lg/xl/
      pill`) token scales added to `globals.css`'s `:root` block, named after the values already
      in real use (4/8/12/14/16/20/24px spacing; 8/9/11/12/100px radii) — additive only, no
      existing hardcoded rule was rewritten to consume them (that would be a large, purely-
      cosmetic diff with no behavior change; the scale now exists for new/touched rules to
      reference instead of picking another ad-hoc number).~~
- [x] ~~**T2** — micro-text font-size scale (`--text-2xs: 10px`, `--text-xs: 11px`,
      `--text-sm: 12px`) added to `globals.css`'s `:root` block, same additive-only rationale as
      T1.~~
- [x] ~~**T3** — new `web/src/lib/workspace-theme.ts` (`WORKSPACE_BG`/`WORKSPACE_FG`) +
      matching `--workspace-bg`/`--workspace-fg` CSS custom properties in `globals.css`.
      Re-verified PLAN.md's own claim before building: both hardcoded occurrences of
      `#0a0a0a`/`#e5e5e5` are actually BOTH in `WorkspaceTerminal.tsx` (xterm's `theme` config
      object + the container's `bg-[#0a0a0a]` Tailwind class), not split across
      `WorkspaceEditor.tsx` too as PLAN.md described — `WorkspaceEditor.tsx` uses Monaco's
      built-in `theme="vs-dark"`, no hardcoded hex. Real duplication within one file, now closed:
      xterm's JS config reads the new `lib/workspace-theme.ts` constants (xterm can't resolve CSS
      custom properties, so a JS-side mirror is required), the Tailwind class reads the new CSS
      var. 1 new test pinning both literal values so the two can't silently diverge again.~~
- [x] ~~**U3** — new `web/src/lib/idempotency.ts`: `makeIdempotencyKey(scope)`. Confirmed via
      grep exactly 1 real call site as PLAN.md flagged. Migrated
      `catalog/[activityVersionId]/page.tsx`'s attempt-start mutation, preserving the exact
      original output format (`web-<scope>-<timestamp>`) by having the caller pass the full
      semantic scope (`start-${activityVersionId}`) rather than baking `'start'` into the
      function itself. 2 new tests, including one pinning the exact original string shape via a
      mocked `Date.now()`.~~
- [x] ~~**C4** — confirmed ALREADY CLOSED, not rebuilt: the byte-for-byte "Could not reach
      practice-core…" duplicate PLAN.md's original audit flagged no longer exists as a
      duplicate — `web/src/lib/error-message.ts`'s `toUserFacingError()` (built during Phase 3
      standardization, per `PHASE3_STANDARDIZATION.md`) already centralizes this decision logic,
      consumed via `components/ui/Alert.tsx` at both real call sites. No further work needed
      beyond K1's incidental migration of this file's own `API_BASE_URL` read.~~
- [x] ~~**C6** — new `web/src/components/ui/SectionLabel.tsx`: polymorphic `as` prop
      (`h2`/`h3`/`p`/`span`/`label`, default `h2`) wrapping `.font-mono-label`. Confirmed via
      grep all 5 element types were in real ad-hoc use across 9 call sites in 7 files before
      migrating (2 more files than PLAN.md's own count — `Sidebar.tsx`'s `label` and
      `catalog/page.tsx`'s domain-section `p` weren't separately named in the original audit).
      Migrated all 12 real call sites across 6 files: `page.tsx` (3), `catalog/
      [activityVersionId]/page.tsx` (3), `catalog/page.tsx` (1), `history/page.tsx` (1),
      `attempts/[id]/page.tsx` (3), `Sidebar.tsx` (1). Confirmed via grep zero `font-mono-label`
      usages remain outside
      `globals.css`'s own definition and `SectionLabel.tsx` itself. 5 new tests
      (`SectionLabel.spec.tsx`), including one confirming `htmlFor` is passed only when
      `as="label"` and doesn't leak onto other tags.~~

**Web verification**: `tsc --noEmit` clean; full test suite 95/95 passing (74 pre-existing + 21
new); `next build` production build clean (caught and fixed one incidental issue: an early
version of a doc comment in `mastery.ts` contained the literal text `bg-[var(--*)]` as a written-
out example, which Tailwind's build-time content scanner matched as a real class usage and failed
to parse — fixed by rephrasing the comment, not by the actual component code, no functional bug).
Live-verified against the real dev server (`http://localhost:3000`, cache cleared and restarted
after the CSS-comment fix): all 4 top-level routes (`/`, `/catalog`, `/skills`, `/history`) return
200; `catalog?course=devops-with-ai` and `catalog?course=genai-with-ml` server-render the correct
`courseLabel()` output ("DevOps With AI") confirming K3/K4 both work end-to-end; home page's
rendered HTML contains the `font-mono-label` class confirming `SectionLabel` renders correctly.

### practice-core

- [x] ~~**U6** — new `practice-core/src/common/base.repository.ts`: abstract `BaseRepository`
      class owning the `@Inject(KYSELY) protected readonly db: Kysely<Database>` constructor.
      Confirmed via grep the real scope first, not every `@Inject(KYSELY)` site: 17 files inject
      `KYSELY`, but only 5 are genuinely pure repositories with a `KYSELY`-only constructor
      (`SkillRepository`, `CurriculumRepository`, `CatalogRepository`, `EventStoreRepository`,
      `AttemptRepository`) — the other 12 are services with additional real dependencies
      alongside `KYSELY` and correctly left untouched, not forced to extend a repository base
      they aren't one. All 5 migrated to `extends BaseRepository`, each dropping its own
      duplicate constructor line and now-unused `Kysely`/`Database`/`Inject`/`KYSELY` imports
      (kept `Database`/`Transaction` imports where still genuinely used elsewhere in the file,
      e.g. `CatalogRepository`'s `Transaction<Database>` parameter type).

      A unit test was attempted (`base.repository.spec.ts`, constructing `BaseRepository`
      directly with a fake `db`) but hit the identical pre-existing constraint Phase 3's S8
      documented: importing `database.module.ts` for the `KYSELY` token transitively pulls in
      the real `kysely` package, which the default unit-test Jest config (no
      `transformIgnorePatterns` override, unlike `test/jest-integration.json`) can't parse
      (`SyntaxError: Unexpected token 'export'`). Followed the same precedent S8 established
      rather than touching the shared Jest config for one new test: deleted the unit spec:
      real DB-backed repositories in this codebase are exercised through the integration suite
      exclusively (confirmed via grep — `FaultRepository`/`RubricRepository` are the only
      repositories with unit specs, and both are file-based, not `KYSELY`-backed, so they never
      hit this constraint).

      Verification: `tsc --noEmit` clean. Full unit suite 152/152 passing unchanged (no new unit
      test, per the constraint above). Full integration suite 78/78 passing — all 5 migrated
      repositories are genuinely exercised against real Postgres (`AttemptRepository`/
      `CatalogRepository`/`EventStoreRepository` heavily, across nearly every integration file;
      `SkillRepository` across 8 files; `CurriculumRepository` in `recommendation.integration.
      spec.ts`), confirming `BaseRepository`'s DI wiring (`this.db` correctly resolves through
      the inherited constructor) works end-to-end, not just compiles. Live-verified: the running
      `nest start --watch` process rebuilt cleanly (confirmed via `dist/src/common/
      base.repository.js` mtime) and continues serving real HTTP requests (401 on an
      unauthenticated request, not a 500 — confirms no DI/boot regression from the 5 changed
      constructors).~~

### orchestrator

**Resumed and completed (2026-08-24).** Before touching anything, re-confirmed the other
session's work had settled: `provision.go` (the file mid-edit at pause time) hadn't changed on
disk in ~44 minutes, and `go build`/`go vet`/`go test ./...` were all clean against the other
session's current state. Re-verified this session's own earlier work (S7/K7/K12/U6 in
practice-core) was intact and had been correctly built upon, not overwritten, by the other
session's concurrent edits before resuming.

- [x] ~~**K13** — re-grepped fresh (counts had shifted from the paused state, confirming the file
      really had changed): `provision.go` still had the 3 consts and its own call sites intact
      (survived the other session's 436-insertion rewrite). Migrated the remaining 5 real
      `"workspace"` occurrences within `provision.go` itself that were missed at pause time
      (volume name, pod name, pod label, `Pods().Get()` lookup, Service name/selector — all
      confirmed the same logical identity via re-reading context, not assumed). Then migrated
      the 5 other files: `sessionbroker/broker.go` (2 sites), `sessionbroker/session_registry.go`
      (2 sites), `validation/validation.go` (2 sites, already imported `internal/k8s` as `k8s`).
      Deliberately did NOT migrate `idledetect/detector.go`'s 1 remaining site
      (`PodMetricses(ns).Get(ctx, "workspace", ...)`) — that package's own doc comment states a
      real, deliberate architectural boundary ("DestroyFunc mirrors costmeter's pattern: idle
      detection triggers a snapshot+suspend+destroy, not a direct k8s import here"), and forcing
      an `internal/k8s` import there for one string constant would contradict that stated design
      choice, not improve it. `orchestrator/credentials.go`'s 2 occurrences are doc-comment prose
      only, not real code duplication. 10 of 11 real occurrences migrated; 1 left as a documented
      exception. `go build`/`vet`/`gofmt`/`test` all clean.~~
- [x] ~~**U13** — new `k8s.ObjectMeta(name, ns)` in `provision.go` (2-field constructor;
      confirmed via re-reading each site that the 2 other `ObjectMeta` literals in this file
      that also set `Labels` — `createNamespace`'s Namespace object, `createWorkspacePod`'s own
      Pod — are a genuinely different 3-field shape and correctly excluded). Migrated all 6 real
      2-field call sites (`applyResourceQuota`, `applyLimitRange`,
      `applyDefaultDenyNetworkPolicy`, `applyEgressProxyAllowlist`, `applyServiceAccount`,
      `ExposeWorkspaceService`). 2 new tests (`object_meta_test.go`), including a regression
      guard confirming the helper never sets `Labels`/`Annotations`.~~
- [x] ~~**K16** — new `ManagedNamespaceLabelKey`/`Value`/`Selector` consts in `provision.go`.
      `createNamespace`'s label map and `ListManagedNamespaces`'s hand-built selector string
      (`"practiceengine.dev/managed=true"`) now both derive from the same two consts instead of
      being independently written. Confirmed via grep no `manifests/*.yaml` reference this label
      (no drift there to also fix). 2 new tests.~~
- [x] ~~**K17** — real, previously-unverified security fix, not just an extraction. Found
      `internal/config/config.go`'s `WSGatewayJWTSecret` falling back to a committed 64-char hex
      literal (`774454b8...`) whenever `WS_GATEWAY_JWT_SECRET` was unset — a real-looking secret
      in source control, and one that silently prevented `wsgateway.NewTokenValidator`'s own
      `panic("requires a non-empty secret")` guard from ever firing. Confirmed practice-core's
      `AuthModule` already throws on a missing `JWT_SECRET` rather than degrading to a shared
      default — matched that same fail-loud pattern here: changed the fallback to `""`. Confirmed
      safe against the real running orchestrator process: its actual `.env` already sets
      `WS_GATEWAY_JWT_SECRET` explicitly, so removing the code-level fallback doesn't change its
      behavior, only closes the gap for a deployment that forgets to set it. Updated
      `config_test.go`'s existing K14-era test (which had pinned the hex literal itself) to assert
      empty instead, and `.env.example`'s comment to state the var is now required, not merely
      recommended. Did not restart the live shared orchestrator process to avoid disrupting the
      other session's concurrent work against it — `go test ./...` (including
      `wsgateway`'s own panic-guard test) is sufficient verification for a config-default change
      with no behavior change against the real `.env`.~~
- [x] ~~**K18** — new `internal/ttl` leaf package (6 real values, not the plan's original loose
      "TTL constants" framing — confirmed via grep the exact 6: `EnvironmentDefault` 90min,
      `IdleTimeoutDefault` 15min, `WarmPool` 30min, `SessionToken` 30min, `ValidatorCredential`
      5min, `FixtureToken` 4h). Migrated all 6 real sites across 5 files (`orchestrator/server.go`
      x3 — including renaming a local `ttl` variable to `envTTL` to avoid shadowing the new
      package import within the same function, caught by `go build` immediately, not shipped
      broken — `warmpool/manager.go`, `wsgateway/gateway.go`, `fixture/handlers.go`). 6 new tests
      pinning every value plus 2 invariant tests (`WarmPool`/`SessionToken` both genuinely shorter
      than `EnvironmentDefault`, not just coincidentally smaller today).~~
- [x] ~~**K19** — new `internal/destroyreason` leaf package (`Idle`/`Budget`/`Reaper`/`Submit`).
      Real import-cycle constraint found and resolved, not guessed around: `internal/orchestrator`
      already constructs `idledetect.Detector`, so `idledetect` importing `internal/orchestrator`
      back for the const would be a real cycle — resolved by putting the consts in their own leaf
      package (matching `internal/ttl`'s precedent) rather than in `internal/orchestrator` as
      first drafted, caught before it ever failed to compile by checking import direction first.
      Migrated the 3 real Go-side producer sites (`idledetect/detector.go`, `main.go`'s
      budget-hard-stop closure, `reaper/reaper.go` x2). `Submit` is documented as never
      constructed by this codebase (practice-core supplies it over the wire via
      `DestroyRequest.Reason`) but named anyway so the full value set has one source of truth. 2
      new tests (exact-value pinning + a distinctness check).~~
- [x] ~~**K20** — two genuinely different concepts, handled separately per PLAN.md's own framing.
      Validator-result status (`PASS`/`FAIL`/`ERROR`): new `StatusPass`/`StatusFail`/`StatusError`
      consts in `internal/validation` (the package `Result.Status` already lives in) — 35 real
      occurrences in `handlers.go` (verified each was a genuine `Status:`/`status :=`/`status =`
      assignment before a mechanical `sed` replace, not blindly trusting the count) plus 1 in
      `server.go`'s `ExecValidator` error path, all migrated. Environment lifecycle status
      (`READY`/`DESTROYED`): confirmed these are Postgres SQL string literals embedded in raw
      query text, not Go-level values (`env.environment.status` is a schema-unenforced plain
      `text` column, confirmed via the migration SQL — zero CHECK constraint or DB enum exists
      anywhere for this value either). New `internal/envstatus` leaf package (same cycle
      reasoning as K19: `orchestrator` imports `warmpool`, so the reverse can't hold the shared
      const). Migrated by converting the 3 real inline SQL literals (`server.go`'s Provision
      UPSERT, `warmpool/manager.go`'s Filler UPSERT, `destroyer.go`'s teardown UPDATE) to
      parameterized query args instead of leaving them as un-checkable inline SQL text — a small
      but real query-shape change, verified behavior-neutral via the full `go test ./...` run
      (no test asserts raw SQL text, and the query's logical effect — which columns get which
      values — is unchanged). 3 new tests total (2 status consts, 1 envstatus consts).~~

Final verification for the whole orchestrator section: `go build ./...`, `go vet ./...`,
`gofmt -l .` (empty), and `go test ./...` all clean across every package, including the 4 new
leaf packages this pass added (`internal/ttl`, `internal/destroyreason`, `internal/envstatus`,
plus extensions to the existing `internal/k8s` and `internal/validation`). A full standalone
binary build (`go build -o /tmp/orchestrator-verify ./cmd/orchestrator`) also succeeded, confirming
the whole dependency graph links correctly end-to-end.

---

## Verification gate (every item, before marking done)

- Functional: existing test suites still pass (`web`: `npm test`/vitest; `practice-core`:
  `npx jest` + integration suite; `orchestrator`: `go test ./...`) AND new tests cover the
  extracted abstraction / fixed bug in isolation.
- S7 specifically needs a dedicated regression test asserting the new `ConflictException`
  behavior on a concurrent version mismatch — this is the one item in this file with an
  observable API-behavior change, matching PLAN.md's own flag.
- Live verification against the real running stack where observable end-to-end (web dev server,
  practice-core :3001, orchestrator :50051), matching this session's established standard.
- No item in this phase should leave a call site half-migrated — every existing usage of the old
  inline/duplicated pattern gets swapped to the new shared symbol, not just the new symbol added
  alongside the old one.
