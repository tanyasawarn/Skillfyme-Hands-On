# PLAN.md Phase 5 — Cleanup and Documentation

Tracks the 3 tasks from `PLAN.md`'s "Phase 5 — Cleanup and documentation" section (lines
322-325), the final phase of the centralization plan. Phases 0-4 were verified complete on
2026-08-24 (all items checked directly against real code, not against any tracking file's
claims — see the conversation history for that audit). This file tracks Phase 5 specifically.

**Status: 3/3 tasks complete.** Dead-code sweep found zero leftovers across all three services
(every suspicious grep hit resolved to either a doc-comment reference or a documented, deliberate
exception — nothing needed deleting). Two new READMEs added (`web/src/components/ui/README.md`,
`practice-core/src/common/README.md`), each with a real, verified inventory of the directory's
actual current contents, not a copy of PLAN.md's originally-proposed file list. All 14 "top
opportunity" items from PLAN.md's own citations re-verified closed via direct grep/Read against
current files. Full test suites clean: practice-core 152/152 unit, orchestrator 15/15 packages
(build/vet/gofmt all clean), web 95/95. This closes out PLAN.md's Phase 5 — all 6 phases (0
through 5) of the centralization plan are now complete.

**Rule for this file**: no item gets checked off from reading a doc comment, a prior session's
claim, or code that merely *describes* an intent. Each item is checked off only after direct
verification against the real, current file state (`ls`, `grep`, `Read`, and where applicable
`npm test` / `go test`) confirms the work is actually done — not before.

---

## Task 1 — Remove now-dead code

> "Remove now-dead code (the inline implementations Phase 1-4 replaced, if any were left in
> place for a transition period)."

This requires an actual sweep, not an assumption that Phases 1-4 were clean. Scope: every real
extraction target from PLAN.md's Section 1 inventory (45 items, C1-C9/T1-T4/U1-U13/S1-S8/K1-K20)
must have zero surviving duplicate/dead inline implementation anywhere in `web/`, `practice-core/`,
or `orchestrator/`.

- [x] ~~**web** — grepped for every leftover pattern: `BAND_COLOR`/`COURSE_TITLE`/`BAND_VARIANT`
      as live identifiers (zero hits — only doc-comment references to the old names for
      historical context, in `courses.ts`/`mastery.ts`), `.skill-card-badge` as a live CSS class
      (zero — only comments), `font-mono-label` used bare outside `SectionLabel.tsx`/
      `globals.css` (zero — all 12 real call sites migrated), `NEXT_PUBLIC_API_BASE_URL` read
      outside `lib/config.ts` (zero), raw `fetch()` outside the api layer (the 2 hits outside
      `api-client.ts` are legitimate: `auth-token.ts`'s dev-login bootstrap call, which must run
      *before* a token exists so can't go through `request()`; `error-message.ts`'s hit is a
      comment). `isLoading &&`/`isError &&` in 3 files (`page.tsx`, `attempts/[id]/page.tsx`,
      `skills/page.tsx`) already use the shared `Loader` component and have no `isError` branch
      at all — matches Phase 4's own documented C8 scoping (only `catalog/page.tsx` and
      `history/page.tsx` had the exact duplicated inline pattern `QueryBoundary` targets; these 3
      use a different, deliberately-kept early-return shape). **Zero dead code found.**~~
- [x] ~~**practice-core** — grepped every "not found" throw site (18 real occurrences across 12
      files): confirmed each is either a `findOrThrow()` call site (verified via direct
      `grep -c findOrThrow` per file — all 6 files with "not found" messages have matching
      `findOrThrow` usage) or one of the 2 documented, deliberate exceptions from the original
      U5 work (`admin.controller.ts`'s `assertActivityInCallerTenant`, a security-motivated
      combined tenant+existence check that can't route through `findOrThrow` without losing its
      "don't leak cross-tenant existence" framing; `catalog.repository.ts`'s optimistic-
      transition-conflict check, which throws `ConflictException` for a different semantic, not
      "not found"). All 12 files with `@Inject(KYSELY)` are services with additional real
      dependencies beyond `KYSELY` (confirmed against U6's own documented exclusion list), not
      leftover un-migrated repositories. All 4 real S5 target sites
      (`artifact.service.ts`, `command-executed.consumer.ts`, `hint.service.ts`) use
      `ActivitySpecReader.getActivitySpec()`; the remaining `spec_jsonb` reads in
      `attempt.service.ts`/`evaluation.service.ts`/`catalog.repository.ts` are each a separate,
      wider query needing other columns too (documented S5 exclusions, not duplicates). Zero
      `ArtifactSpecEntry`/`ActivityTaskSpec`/`ProcessSignalsSpec` (the old K10 ad-hoc types)
      anywhere in `src/`. **Zero dead code found.**~~
- [x] ~~**orchestrator** — grepped for every leftover pattern: bare `"workspace"`/`"shell"`
      string literals as live values (1 confirmed occurrence, `idledetect/detector.go`'s
      `PodMetricses(ns).Get(ctx, "workspace", ...)` — the single documented K13 exception per that
      package's own stated "no k8s import here" architectural boundary; all other hits are
      comments), `strategicMergePatch` (zero — fully deleted per U11's original work), raw
      `os.Getenv`/`getEnv*` calls in `main.go` outside `config.Load()` (zero — the one grep hit
      is a comment describing the old pattern), the committed JWT-secret hex literal
      `774454b8...` (zero occurrences anywhere in source, confirmed the K17 fix is real and
      complete). One near-miss investigated and ruled a false positive: `provision.go:349`'s
      `resource.MustParse("2")` for `corev1.ResourceServices` — a K8s Service-object *count*
      quota, an unrelated concept that coincidentally shares the digit "2" with
      `DefaultT1Resources.CPU`, correctly not consolidated. **Zero dead code found.**~~
- [x] ~~Confirmed via direct `grep -c`/`grep -rn` counts across all three services, not spot
      checks alone — every category above returned either 0 live hits or hits that resolved to
      documented, deliberate exceptions on inspection.~~

## Task 2 — Shared-code convention documentation

> "Add a short `web/src/components/ui/README.md` and equivalent notes in
> `practice-core/src/common/` documenting what lives there and the 'put shared things here, not
> in a module' convention, so the audit's root cause (nobody had a designated place to put
> shared code) doesn't recur."

- [x] ~~**`web/src/components/ui/README.md`** — created. States the presentation-only/no-
      data-fetching rule, why it exists (the 119-finding audit, the two live bugs it caused),
      and a real inventory of all 11 real components currently in the directory (confirmed via
      `ls`, cross-referenced against each file's own top doc comment for an accurate one-line
      purpose) — not PLAN.md's original 9-item proposed list, which predates 2 components that
      were added since (`Button.tsx`, `WithSearchParamsSuspense.tsx`) and never mentions
      `Loader.tsx` under that name (PLAN.md called it `LoadingState.tsx`).~~
- [x] ~~**`practice-core/src/common/README.md`** — created. States the "put shared things here"
      rule and the "organized by what a thing is" principle (PLAN.md §5.2), with a real inventory
      of all 10 real modules currently in `common/` (confirmed via `find`, which caught
      `activity-spec-reader.ts` that a naive glob missed) — described the directory's real, flat
      structure rather than PLAN.md's originally-proposed `{constants,types,repository,grpc,
      nats,errors,guards}/` subdirectory split, with an explicit note explaining why it stayed
      flat (10 files is small enough that a flat listing beats near-empty subdirectories) so a
      future contributor doesn't wonder if the subdirectories were forgotten. Also documents
      where `AttemptOwnershipGuard`/`Role.isRole()` deliberately live instead (colocated with
      their module, kept dependency-free for Jest-testability) since those are exactly the kind
      of security-adjacent code someone might otherwise expect in `common/` — verified this
      claim directly (`attempt-ownership.guard.ts` imports `AttemptRepository`, importing
      `kysely`; `attempt-ownership.ts` is the separate zero-dependency pure-logic module) before
      writing it, not assumed from a prior session's description.~~
- [x] ~~Orchestrator judgment call: no README added. PLAN.md's Phase 5 task names only
      `web/src/components/ui/` and `practice-core/src/common/` explicitly — orchestrator's
      constant/helper sprawl (K13/K15/K16/U13, all living in `internal/k8s/provision.go`) is a
      single file with its own doc comments at each addition, not an empty-scaffolded-directory
      case like the other two; judged not to need a separate README for this phase.~~
- [x] ~~Verified `tsc --noEmit` clean in both `web/` and `practice-core/` after adding the two
      README files (markdown doesn't affect compilation, but confirms nothing else drifted
      during this task).~~

## Task 3 — Spot-check re-audit against the top opportunities

> "Re-run the original three audit agents' methodology as a spot-check (not full re-audit)
> against the top 15 opportunities to confirm closure."

PLAN.md's own inline citations only actually name 14 distinct "top-opp" items (its own numbering
has gaps at #8 and #14 — never explained in the source doc, not something to invent an answer
for). The real list, extracted directly from PLAN.md's own tags:

| # | Candidate | What it closes |
|---|---|---|
| 1 | K5 | `lib/attempt-status.ts` — the confirmed status-color live bug |
| 2 | K8 | `AttemptEventType` — event-type taxonomy with zero type safety |
| 3 | U8 | `requireEnvironment(ctx, envID)` — verbatim 7× in `server.go` |
| 4 | S8 | `AttemptOwnershipGuard` — ownership check inlined 12× |
| 5 | C1 | `Badge`/`StatusPill` — 3 parallel badge implementations |
| 6 | U7 | `getOrNotFound[T]()` — repeated 8× across fault-injection |
| 7 | S3 + S4 | `BaseGrpcClient` + `NatsSubscriberBase` — duplicated wholesale |
| 9 | K13 | Workspace/shell name consts — repeated 12+ times |
| 10 | K10 | `ActivitySpec` canonical type — redeclared ad-hoc in 7 files |
| 11 | K4 | `lib/courses.ts` — course label map triplicated |
| 12 | K15 | `DefaultT1Resources`/`DefaultT2Resources` — hardcoded in 6+ places |
| 13 | K7 | `AttemptStatusGroups` — second confirmed drift instance |
| 15 | K17 | JWT-secret fallback moved out of source |

- [x] ~~All 14 items directly re-verified against real current files (grep + targeted Reads, not
      trusting any prior phase's own claim):~~

      | # | Item | Verified |
      |---|---|---|
      | 1 | K5 `attempt-status.ts` | Exists; consumed by `history/page.tsx` and `attempts/[id]/page.tsx` |
      | 2 | K8 `AttemptEventType` | Exists (`event-store/attempt-event-type.ts`); 8 real consumer files |
      | 3 | U8 `requireEnvironment` | Method exists on `Server`; exactly 7 real call sites, matching the original audit count |
      | 4 | S8 `AttemptOwnershipGuard` | 13 real `@UseGuards(AttemptOwnershipGuard)` sites; old `assertOwnedByCaller` fully gone (only a doc-comment reference remains) |
      | 5 | C1 `Badge` | Exists; 4 real files import it |
      | 6 | U7 `getOrNotFound[T]` | Generic exists; 9 real call sites across `handlers.go` (5) + `handlers_batch2.go` (4) |
      | 7 | S3+S4 `BaseGrpcClient`/`NatsSubscriberBase` | Both real gRPC clients extend `BaseGrpcClient`; both real NATS consumers extend `NatsSubscriberBase` |
      | 9 | K13 workspace/shell consts | Defined in `provision.go`; consumed across 4 real files (`provision.go`, `sessionbroker/broker.go`, `sessionbroker/session_registry.go`, `validation/validation.go`) — the 1 documented `idledetect` exception confirmed still in place, not silently dropped |
      | 10 | K10 `ActivitySpec` | Exists (`catalog/activity-spec.ts`); 7 real consumer files |
      | 11 | K4 `courses.ts` | Exists; all 3 real original sites (`catalog/page.tsx`, `skills/page.tsx`, `Sidebar.tsx`) consume it |
      | 12 | K15 `DefaultT1Resources`/`DefaultT2Resources` | Defined; 16 references within `provision.go` (well beyond the original "6+ places" estimate) |
      | 13 | K7 `AttemptStatusGroups` | Consumed by both real sites (`attempt.service.ts`'s terminal check, `attempt.repository.ts`'s retry-chain query); zero leftover private `TERMINAL_STATUSES` array |
      | 15 | K17 JWT secret | `config.go`'s fallback is `""`, not a literal; zero occurrences of the old committed hex secret anywhere in `internal/`/`cmd/` |

      **All 14/14 confirmed genuinely closed** — no gap found in this spot-check. Full test
      suites re-run as the closing check: practice-core 152/152 unit tests, orchestrator 15/15
      packages (`go build`/`go vet`/`gofmt` all clean too), web 95/95 tests — all green.

---

## Verification gate (before this file is marked done)

- Every checkbox above reflects a real command's output actually inspected in this session, not
  an assumption carried over from PLAN_PHASE1_2.md, PLAN_PHASE3.md, PLAN_PHASE4.md, or any other
  tracking file.
- `practice-core`: `npx jest` (unit) and `npm run test:integration` clean after any code change
  this phase makes (dead-code removal should be behavior-neutral, but must be proven, not
  assumed).
- `orchestrator`: `go build ./...`, `go vet ./...`, `gofmt -l .`, `go test ./...` clean after any
  code change.
- `web`: `npx tsc --noEmit`, `npm run test` clean after any code change.
- This file's own top section is updated with a final summary only after all three tasks above
  are independently checked off with real evidence.
