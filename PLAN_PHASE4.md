# PLAN.md Phase 4 — Remaining Moderate Items + UI Component Library

Tracks the 17 real items from `PLAN.md`'s "Phase 4 — Remaining moderate items + UI component
library" section (line 317-320): `C1-C3, C5, C7-C9` (web components), `U1, U2, U4, U5`
(utilities), `K11, K14, K15` (config/constants), `S1, S2, S5, S6` (API/service layers).

Verified against actual code state before starting (not assumed): none of these 17 items exist
yet under any name — confirmed via grep across `web/src`, `practice-core/src`,
`orchestrator/` for every candidate's proposed symbol name. `web/`'s test runner gap noted in
PLAN.md §"Existing scaffolding" is already closed (`vitest` present, `components/ui/` already
holds `Loader`/`Alert`/`Button` with specs from earlier phases) — Phase 4's own web items build
on that same `components/ui/` location.

**Status as of 2026-08-24: all 17/17 items complete** (C1/C2/C3/C5/C7/C8/C9, U1/U2/U4/U5,
K11/K14/K15, S1/S2/S5/S6), each with the shared abstraction built, real call sites migrated,
functional tests, and live verification against the real running stack. This closes out
PLAN.md's Phase 4 checklist. Along the way this phase found and fixed 5 real bugs beyond pure
duplication: K5's confirmed status-color disagreement (closed via C1), a missed Loader/Alert
migration site (C2), U4's 3-of-4 wrong directory-depth path resolution (silently masked by a
fallback), U5's 400-vs-404 API inconsistency (standardized on 404 after checking for consumer
dependencies), and K15's T1/T2 resource-default duplication across 4 sites (now enforced as one
invariant, not just documented).

Each item requires: the shared abstraction/component built, existing call sites migrated to use
it, functional tests, and — per the standing session convention — live verification against the
real running stack (web dev server, practice-core on :3001, orchestrator on :50051) where
applicable, not just unit tests in isolation.

PLAN.md's own note: "most of these are 'extract a component/function, swap call sites' with no
ambiguous behavior decision required (unlike Phase 2)" — lower risk per item than Phase 3, but
still real call-site migrations, not just new files sitting unused.

---

## Web — UI components (`web/src/components/ui/`)

- [x] ~~**C1** — `Badge`/`StatusPill`: new `web/src/components/ui/Badge.tsx`, unifying 3
      parallel implementations (`.lms-pill` + per-page status maps, `.skill-card-badge`,
      `TaskStatusIcon`). Discriminated-union props (`PillBadgeProps`/`CircleBadgeProps`) so a
      circle-shape badge can't be given the pill-only `outline` variant -- a real compile error,
      not a runtime guard. **Found and fixed K5's confirmed live bug as part of this** (K5 was
      never separately built): `history/page.tsx` and `attempts/[id]/page.tsx` disagreed on the
      color for 5 of 14 `AttemptStatus` values (`PROVISIONING` accent-vs-warning, `IN_PROGRESS`
      accent-vs-success, `SUSPENDED`/`EXPIRED`/`ABANDONED` warning-vs-muted) -- same status,
      different pill color depending which page you were on. New `web/src/lib/attempt-status.ts`
      (`ATTEMPT_STATUS_META`) is the one canonical map now, with each disagreement resolved and
      documented (accent = in-progress/transient, not yet success or a problem; warning =
      reached a non-error terminal/paused state still worth the learner's attention). Also found
      `.skill-card-badge--locked`'s outline look (transparent bg + border) had no equivalent
      among `.lms-pill`'s 5 variants -- rather than silently changing its appearance, added a
      genuine 6th `outline` variant preserving the exact look (confirmed via user choice, not
      assumed). Migrated all 4 real call sites (`history/page.tsx`, `attempts/[id]/page.tsx` x2 --
      status pill + `TaskStatusIcon`→`TaskStatusBadge`, `catalog/page.tsx` x2 -- skill
      live/locked badge + difficulty pill); removed the now-dead `.skill-card-badge`/
      `--live`/`--locked` CSS from `globals.css`. 10 new tests (4 `attempt-status.spec.ts`, 6
      `Badge.spec.tsx`) -- full web suite 30/30 passing, `tsc --noEmit` clean. No browser
      automation tool is available in this environment, so live visual verification used RTL
      component tests (real render + real class-name assertions in jsdom) plus confirming the
      dev server and practice-core backend are both up and reachable, rather than an actual
      rendered-browser screenshot -- noted explicitly rather than silently claimed as a full
      live check.~~
- [x] ~~**C2** — `PageContainer`: new `web/src/components/ui/PageContainer.tsx`. Real values
      disagreed per page (`max-w-3xl` on 4 pages, `max-w-4xl` on history, `max-w-6xl` on catalog;
      `py-10`/`py-12`/`pb-16` vertical spacing) -- `maxWidth` and a required `spacing` prop
      (not defaulted to one page's choice) rather than hardcoding a single page's values for
      everyone. Migrated all 6 real page-container sites plus 2 Suspense-fallback containers
      (`catalog/page.tsx`, `skills/page.tsx`) across `page.tsx`, `catalog/page.tsx`,
      `skills/page.tsx`, `history/page.tsx`, `catalog/[activityVersionId]/page.tsx`,
      `attempts/[id]/page.tsx` (2 sites: outer container + a `pb-16`-only section wrapper below
      the header). `catalog/[activityVersionId]/page.tsx` additionally had its own
      never-previously-migrated inline loading/error divs (missed by the earlier `Loader`/
      `Alert` extraction pass) -- migrated those to the existing shared components as part of
      this same edit, not left as a 3rd inconsistent pattern.~~
- [x] ~~**C3** — `EmptyState`: new `web/src/components/ui/EmptyState.tsx`. `Loader` already
      covered the loading half (earlier phase); this is the empty-list-message half, hand-rolled
      at 6 real sites. Two real shapes existed, not one -- a plain message line (`variant:
      'message'`, the default) and a bordered card box (`attempts/[id]/page.tsx`'s "No tasks for
      this activity", `variant: 'card'`). `children: ReactNode`, not a `message: string` prop,
      since `history/page.tsx`'s empty state embeds a real `<Link>` inside the message --
      confirmed via a dedicated test that JSX children render correctly, not just plain text.
      `className` fully replaces the default (not appends), matching `Loader`'s own established
      pattern, needed because `page.tsx`'s two nested empty states require `mt-2 text-sm
      text-[var(--ink-soft)]`, not just a different top margin. Migrated all 6 real sites
      (`page.tsx` x2, `catalog/page.tsx`, `history/page.tsx`, `skills/page.tsx`,
      `attempts/[id]/page.tsx`'s TaskRail). Also found and migrated a `BAND_COLOR`-to-raw-
      `lms-pill--*`-string map in `skills/page.tsx` that C1's original survey didn't flag (a
      mastery-band pill, structurally identical to the attempt-status pills C1 already unified)
      -- now uses `Badge`/`BadgeVariant` instead of a hand-rolled `lms-pill ${...}` template
      string. 11 new tests (6 `PageContainer.spec.tsx`, 5 `EmptyState.spec.tsx`) -- full web
      suite 41/41 passing, `tsc --noEmit` clean. All 4 migrated top-level routes (`/`,
      `/catalog`, `/skills`, `/history`) confirmed live via the real dev server: `200 OK`, no
      compile errors (RSC payload's `"error":"$undefined"` markers are Next.js's own
      always-present error-boundary scaffolding, confirmed present identically before any of
      this session's edits too, not a regression).~~
- [x] ~~**C5** — `ProgressBar`: new `web/src/components/ui/ProgressBar.tsx`. Mastery bar
      (`page.tsx`) and criteria-breakdown bar (`attempts/[id]/page.tsx`) share the exact same
      track markup (`h-1.5 flex-1 overflow-hidden rounded-full bg-[var(--inset)]`), differing
      only in fill color (per-row by mastery band vs. fixed accent) and whether the inner fill
      gets `rounded-full`+`transition-[width]` (criteria bar only) -- both are real prop-level
      differences, not accidental duplication, so both stayed configurable (`fillClassName`,
      `animated`) rather than the component silently picking one page's choice. Value is a 0-1
      fraction matching both real call sites' own source values (`p_mastery`, `criterion.value`),
      not a 0-100 percentage. 5 new tests -- caught and fixed a real test-selector bug during
      writing (`div > div` from an RTL `container` matched the track itself, not the fill,
      because `container`'s own direct child IS the track; fixed to `:scope > div > div`) before
      trusting any assertion, not after. Migrated both real call sites. Full web suite 46/46
      passing, `tsc --noEmit` clean, `/` confirmed live via the real dev server (200 OK).~~
- [x] ~~**C7** — `CardLink`: new `web/src/components/ui/CardLink.tsx`. The 3 real
      implementations (`SkillCard`, `HistoryRow`, home dashboard cards) have genuinely different
      internal content layouts (vertical multi-zone card with badges/title/meta/footer, a
      horizontal row, a simple two-line card) -- confirmed via a dedicated survey before
      designing anything, then a user decision to scope `CardLink` to unify only the clickable
      *chrome* (border/radius/background/hover), not dictate content layout, since forcing one
      internal shape onto all 3 would have been a real visual regression, not a simplification.
      3 real chrome variants, not one: `'lift'` (SkillCard's live state -- lift+shadow+
      border-color hover, wraps `.skill-card`/`.skill-card--live`), `'row'` (HistoryRow --
      border-color-only hover, was pure Tailwind with no custom CSS class, kept as Tailwind
      rather than inventing a new globals.css rule for a shape only one caller uses), `'plain'`
      (home dashboard cards -- shadow-only hover, wraps the existing `.lms-card` class, which is
      also used elsewhere for non-card-link static containers and had to keep working
      unchanged). A 4th state, `disabled` (SkillCard's locked variant), renders a real `<div>`
      instead of a `Link` -- an unclickable card is a different DOM element, not just a style
      flag, matching the original's own `if (!available) return <div>...`/`<Link>...` branch.
      Callers still compose their own children exactly as before (badges, text, custom layout);
      only the outer element + chrome classes are shared. 8 new tests, including a real
      `next/link` render check (confirms the component genuinely produces a navigable anchor,
      not just a styled div) and a disabled-state check (confirms no `role="link"` renders when
      disabled). Migrated all 3 real call sites (`SkillCard`, `HistoryRow`, both home dashboard
      card sites in `page.tsx`); removed now-unused `Link` import from `catalog/page.tsx`.
      Full web suite 54/54 passing, `tsc --noEmit` clean, confirmed no leftover raw chrome
      classes remain (only the legitimately-still-composed content-layout classes like
      `.skill-card-top`/`-meta`/`-footer`/`-cta`), all 3 migrated routes (`/`, `/catalog`,
      `/history`) confirmed live via the real dev server (200 OK).~~
- [x] ~~**C8** — `QueryBoundary`: new `web/src/components/ui/QueryBoundary.tsx`. Real
      byte-for-byte duplication existed in exactly 2 of the 6 route files
      (`catalog/page.tsx`, `history/page.tsx`) -- identical inline `{isLoading && <Loader/>}`
      then `{isError && (IIFE calling toUserFacingError -> <Alert>)}`, rendered alongside the
      page's own header/hero rather than replacing the whole page. The other 2 files with
      isLoading/isError (`catalog/[activityVersionId]/page.tsx`, `attempts/[id]/page.tsx`) use a
      different early-return whole-page-replace style already built from the same shared
      `Loader`/`Alert`/`PageContainer` primitives in a legitimately different structural shape
      (nothing to show yet, vs. chrome-up-content-pending) -- confirmed via direct comparison
      before assuming they were the same pattern, then a user decision to scope
      `QueryBoundary` to only the genuinely-duplicated inline shape rather than force both
      styles into one component (the same near-miss C7 avoided: unifying things that only
      superficially look similar). Success-case content stays as `children`, composed by each
      caller exactly as before -- this component only owns the loading/error rendering, matching
      the real duplication precisely rather than inventing a broader data-fetching abstraction.
      4 new tests. Migrated both real sites (`catalog/page.tsx`, `history/page.tsx`), removing
      the now-unused `toUserFacingError`/`Alert` imports from both (still correctly imported in
      the 2 untouched early-return pages). Full web suite 58/58 passing, `tsc --noEmit` clean,
      both migrated routes (`/catalog`, `/history`) confirmed live via the real dev server (200
      OK).~~
- [x] ~~**C9** — `WithSearchParamsSuspense`: new `web/src/components/ui/
      WithSearchParamsSuspense.tsx`. `catalog/page.tsx` and `skills/page.tsx` each split real
      content into an `...Inner` component and wrapped an identically-shaped `<Suspense
      fallback={<PageContainer><Loader/></PageContainer>}>` default export around it, differing
      only in the fallback's maxWidth/spacing/label -- a plain component taking `Component`,
      `loadingLabel`, `maxWidth`, `spacing` as props (not a HOC function returning a component),
      so it's testable with the same `render()` pattern as every other shared UI piece rather
      than needing special "returns a component" test setup. 4 new tests, including a real
      suspend-then-resolve check (a test component that genuinely throws a pending promise on
      first render, matching what `useSearchParams()` triggers during prerender -- proves the
      fallback renders under an actual Suspense boundary, not just that the fallback JSX is
      syntactically reachable) -- caught and fixed a real type error while writing it (a
      component that only `throw`s has an inferred return type of `void`, not `ReactNode`,
      which `ComponentType` rejects; fixed with an explicit `: ReactNode` return annotation).
      Migrated both real sites; removed now-unused `Suspense`/`Loader`/`PageContainer` imports
      from `catalog/page.tsx` (all three were only ever used inside the removed inline Suspense
      wrapper) while correctly keeping `Loader`/`PageContainer` in `skills/page.tsx`, which still
      uses both for its own inline `isLoading` state, separate from the Suspense fallback. Full
      web suite 62/62 passing, `tsc --noEmit` clean, both migrated routes (`/catalog`, `/skills`)
      confirmed live via the real dev server (200 OK), confirmed no raw `<Suspense>` usage
      remains anywhere in `src/app`.~~

## Web — Utilities (`web/src/lib/`)

- [x] ~~**U1** — `formatPercent()`/`formatCurrency()`: new `web/src/lib/format.ts`. 6 real
      percent-formatting sites used two nominally-different strategies (`.toFixed(0)` vs
      `Math.round(...)`) that produce identical output for every real (always >=0) value these
      call sites pass -- confirmed via direct numeric comparison before assuming a live
      divergence, so this wasn't a K5/S7-class live bug. Both independently share a real, if
      rare, cosmetic bug: a value close enough to zero renders as `"-0%"`
      (`(-0.001).toFixed(0)` -> `"-0"`; `Math.round(-0.4)` -> `-0`) -- `formatPercent` fixes this
      once for every call site (`rounded === 0 ? 0 : rounded`), verified via a dedicated test.
      `value` is a 0-1 fraction (matching what every real source value -- `p_mastery`,
      `final_score`, `penalty`, `criterion.value` -- actually is); one call site
      (`attempts/[id]/page.tsx`'s `scorePct`) pre-multiplied by 100 into its own variable before
      formatting, an inconsistent style choice rather than a different data shape -- removed the
      intermediate variable and passed the real 0-1 source through, one less redundant local. 6
      new tests. Migrated all 6 real percent sites (`page.tsx`, `history/page.tsx`,
      `attempts/[id]/page.tsx` x3, `skills/page.tsx`) and both currency sites
      (`catalog/page.tsx`, `catalog/[activityVersionId]/page.tsx`).~~
- [x] ~~**U2** — `formatMode()`: added to `web/src/lib/format.ts`. `.replace('_', ' ')`
      (first underscore only) vs `.replace(/_/g, ' ')` (every underscore) were both in real use
      across the 4 real mode-formatting sites -- confirmed via direct comparison that every real
      mode value today (`GUIDED_LAB`/`PRODUCTION_SIM`/`PROJECT`) has exactly one underscore, so
      output is currently identical either way, not a live bug yet -- but a genuine correctness
      landmine PLAN.md correctly flags HIGH: any future 2+-underscore mode value would silently
      render wrong on whichever call sites kept the single-replace form.
      `attempts/[id]/page.tsx`'s separate `key.replace(/_/g, ' ')` (a scoring-criterion key like
      `technical_correctness`, not an attempt mode) confirmed out of scope -- a different concept
      that happens to use the same regex, not another U2 site. 2 new tests, including a
      `MULTI_PART_LAB`-shaped positive control proving the fix actually matters, not just that it
      compiles. Migrated all 4 real sites (`page.tsx`, `catalog/[activityVersionId]/page.tsx`,
      `attempts/[id]/page.tsx`, `history/page.tsx`). Full web suite 68/68 passing, `tsc --noEmit`
      clean, confirmed zero leftover raw `toFixed`/`Math.round`/`.mode.replace` in `src/app`,
      `/` and `/attempts/x` confirmed live via the real dev server (200 OK).~~

## Web — API/service layers (`web/src/lib/` or a new hooks location)

- [x] ~~**S1** — `useActivityVersion(id)`: new `web/src/lib/use-activity-version.ts`.
      `history/page.tsx` needed the query *descriptor* itself (not a called hook), since it
      batches N of these through `useQueries()` -- one per distinct `activity_version_id` on the
      page -- so `activityVersionQueryOptions(id)` is exported separately and
      `useActivityVersion` (the single-id hook the other 2 sites use) is built on top of it,
      rather than the batched site needing its own 4th re-inlining of the shape. `id` takes
      `string | undefined` (matching `attempts/[id]/page.tsx`'s real case, where
      `attempt?.activity_version_id` isn't known until the attempt itself loads) --
      `enabled` always ANDs in `!!id` after spreading any caller-passed `options`, so a
      possibly-undefined id can never fire a request even if a future caller's own `enabled`
      override forgets to guard for it (verified with a dedicated test passing
      `enabled: true` alongside `id: undefined` and confirming the query still never fires). 4
      new tests using `renderHook`+`QueryClientProvider` (no existing precedent for testing a
      `useQuery`-based hook in this codebase; established the pattern here) plus a spy on the
      real `api.getActivityVersion`, not a fake — confirms the hook's `enabled` computation is
      really wired, not just plausible-looking. Migrated all 3 real sites
      (`catalog/[activityVersionId]/page.tsx`, `attempts/[id]/page.tsx`, `history/page.tsx`'s
      `useQueries` batching).~~
- [x] ~~**S2** — `useAttemptAction(id, apiFn)`: new `web/src/lib/use-attempt-action.ts`.
      `startMutation`/`submitMutation` in `attempts/[id]/page.tsx` were identical --
      `useMutation({ mutationFn: () => apiFn(id), onSuccess: () =>
      queryClient.invalidateQueries({queryKey:['attempt',id]}) })` -- differing only in which
      API call they wrap. The same file's `revealMutation` is NOT a 3rd instance of this
      pattern -- its `onSuccess` updates local component state (`setRevealed`), not
      `invalidateQueries`, a genuinely different shape confirmed by reading it before assuming
      it belonged here too. 2 new tests confirming both the real `apiFn` call and the real
      `invalidateQueries` call happen (spying on the actual `QueryClient` instance, not
      asserting on mock call counts alone). Migrated both real sites; removed the now-fully-
      unused `queryClient`/`useQueryClient` from the component (only those 2 mutations ever
      read it).~~

## practice-core — Utilities

- [x] ~~**U4** — `resolveRepoRelativePath(callerDirname, dirnameUpLevels, subpath)`: new
      `practice-core/src/common/repo-relative-path.ts`. Found in 4 real files (not 5 --
      confirmed via exhaustive grep before assuming the plan's count), all sharing the identical
      "try `__dirname`-relative first, fall back to `process.cwd()`-relative" shape.

      **A real, live-verified bug found during this extraction, not just style duplication**:
      `base-grpc-client.ts` (at `src/common/`) correctly used 4 `../` segments to reach the repo
      root from its own compiled `dist/src/common/` location -- but the other 3 files
      (`spec-lint.service.ts`, `fault.repository.ts`, `rubric.repository.ts`, all one directory
      deeper at `src/modules/*/`) were copy-pasted with the SAME 4-segment count, confirmed via a
      direct `path.resolve()` check against the real `dist/` layout to resolve to a nonexistent
      `practice-core/contracts`/`practice-core/content/*` path -- `spec-lint.service.ts` even had
      its own comment asserting "repo root is 4 levels up," stated as fact, also wrong. All 3 have
      been silently masked in practice by falling through to their `fromCwd` fallback on every
      real call (works today purely because the app always happens to run with `practice-core/`
      as cwd) -- fragile, not actually fixed, exactly the kind of bug this extraction exists to
      close for good. Fixed with the correct 5-level count for all 3 `src/modules/*/` sites.

      `dirnameUpLevels` is a required parameter, not defaulted or auto-computed, precisely
      because getting this number right per caller is what the 3 broken copies got wrong -- a
      shared function with a silently-wrong default would just move the same bug to one place.
      4 new tests, including one that caught and fixed 2 of my own wrong assumptions about
      `__dirname`/`process.cwd()`'s real values under `ts-jest` before trusting the test (first
      attempt assumed `package.json` lived one level above `practice-core/`, actually lives
      directly in it; second attempt miscounted `__dirname`'s real depth) -- resolved by adding
      debug output and checking the real values live rather than guessing twice.

      Migrated all 4 real call sites. Full unit suite 138/138 passing (134 previous + 4 new),
      integration suite 72/72 passing, `npx tsc --noEmit` clean. **Live-verified against the
      real running stack**: confirmed the compiled `dist/` output has the corrected
      `dirnameUpLevels` values at all 4 sites; directly verified via a real
      `resolveRepoRelativePath()` call against the real `dist/` `callerDirname` that the fix
      resolves correctly via the primary `fromDirname` path alone (not relying on the `fromCwd`
      fallback that was masking the bug); `POST /v1/admin/activities/lint` against the real
      server confirmed `spec-lint.service.ts`'s fix loads and validates the real schema
      correctly; directly instantiated the real compiled `RubricRepository`/`FaultRepository`
      and called their real methods (`getRubric('rub.incident-note.v2')`,
      `getCanonicalDiagnosticPath('f.k8s.memory-limit-too-low')`), both returning genuine
      real content.~~
- [x] ~~**U5** — `findOrThrow(row, message)`: new `practice-core/src/common/find-or-throw.ts`.
      Found 10 real call sites across 6 files (`attempt.service.ts` x5,
      `hint.service.ts`/`workspace-file.service.ts`/`artifact.service.ts`/
      `curriculum.controller.ts`/`catalog.controller.ts` x1 each) -- the confirmed real
      inconsistency: 6 sites threw `BadRequestException` (400) for "attempt X not found," while
      3 threw `NotFoundException` (404) for the same semantic condition on different resources.
      `all-exceptions.filter.ts`'s own doc comment establishes every thrown `HttpException`
      status as an intentional, safe-to-surface decision -- meaning this was a real, if minor,
      API contract inconsistency observable by callers, not just an internal style
      difference. **User-confirmed direction**: standardize on `NotFoundException`/404 (the
      semantically correct status for "resource does not exist") rather than preserve the
      existing split -- checked first for any test or frontend dependency on the specific 400s
      (none found via grep across `web/src`, `test/integration/`) before committing to the
      change. `findOrThrow` only treats `null`/`undefined` as missing (not falsy-but-valid
      values like `0`/`''`/`false`), verified via a dedicated test -- confirmed equivalent to
      every real call site's original `!row` check since all 10 come from Kysely's
      `.executeTakeFirst()`, which only ever returns the real row or `undefined`, never a falsy
      primitive.

      One combined check deliberately left unmigrated:
      `admin.controller.ts`'s `assertActivityInCallerTenant` (`!activity || activity.tenant_id
      !== auth.tenantId`) is a genuinely different, security-motivated shape -- its own doc
      comment explains it deliberately returns the same "not found" message whether the activity
      is missing or just belongs to a different tenant, to avoid leaking cross-tenant existence
      to a caller with no right to know it (the same reasoning that kept S8's
      `AttemptOwnershipGuard` a separate concern in Phase 3) -- forcing it through `findOrThrow`
      would either lose that security framing or require an awkward composition, so it stays as
      its own explicit check.

      4 new tests. Migrated all 10 real sites. Full unit suite 142/142 passing (138 previous + 4
      new), integration suite 72/72 passing, `tsc --noEmit` clean, confirmed no leftover raw
      `if (!x) throw new (BadRequestException|NotFoundException)(...not found...)` pattern
      remains for any migrated resource. **Live-verified the real status-code change against the
      real running server**: `GET /v1/practice/activities/{nonexistent-id}`
      (`catalog.controller.ts`) and `GET /v1/practice/courses/{nonexistent-slug}`
      (`curriculum.controller.ts`) both confirmed real `404 Not Found` with the correct message
      (previously `404` already for these 2, confirming no regression); the 6
      previously-`400` "attempt not found" sites are additionally protected by S8's
      `AttemptOwnershipGuard` on every real route (confirmed live: a request for a nonexistent
      attempt id correctly returns `403 Forbidden` "attempt does not belong to caller" before
      ever reaching the migrated `findOrThrow` code, by design -- the guard's own doc comment
      explains it deliberately never distinguishes missing-vs-not-owned).~~

## practice-core — API/service layers

- [x] ~~**S5** — `ActivitySpecReader.getActivitySpec(attemptId)`: new
      `practice-core/src/common/activity-spec-reader.ts`. Found 4 real sites sharing the exact
      attempt->activity_version->spec_jsonb join, all keyed by `attemptId`, all reading only
      `spec_jsonb` (`hint.service.ts`, `artifact.service.ts` x2, `command-executed.consumer.ts`)
      -- distinct from `attempt.service.ts`'s/`evaluation.service.ts`'s own `selectAll()` version
      lookups, which read other columns (`blueprint_id`, `mode`) too and stayed as their own
      queries rather than being forced through this narrower method. Correctly built on Phase
      3's K10 `ActivitySpec` type as the return type, not another ad-hoc cast.

      **A real architectural constraint found and resolved mid-implementation, not assumed
      away**: the natural home for this (a method on `AttemptRepository`) would need
      `EvaluationModule`'s `ArtifactService` to inject `AttemptRepository`, but `AttemptModule`
      already imports `EvaluationModule` (for `EvaluationService`) -- the reverse import would
      be a circular module dependency, a real NestJS error, not a style concern. **User-confirmed
      direction**: extracted to a standalone `ActivitySpecReader` provider in `common/`, registered
      in both `AttemptModule`'s and `EvaluationModule`'s own `providers` arrays (each module gets
      its own instance -- fine, since it's stateless) -- same reasoning `BaseGrpcClient`/
      `NatsSubscriberBase` already live in `common/` rather than any one feature module.

      Returns `undefined` on a missing attempt rather than throwing -- `command-executed.
      consumer.ts` (a NATS consumer processing a possibly-stale/deleted attempt) needs to return
      `null` and move on, not throw; the 3 sites that DO need a hard failure wrap this in
      `findOrThrow` (U5) at their own call site. One real behavior nuance caught and preserved,
      not silently changed: `artifact.service.ts`'s `gradeArtifact()` previously used
      `.executeTakeFirstOrThrow()` for its own attempt lookup (a real throw on a missing attempt,
      caught by `evaluate()`'s own try/catch and logged as a warning, never learner-facing) --
      an earlier draft of this migration accidentally changed that to a silent `return null`,
      caught by re-reading the caller's own doc comment before trusting the change, and fixed
      back to `findOrThrow` so the throw-and-log behavior is preserved exactly.

      `submit()`'s own separate "attempt not found" existence check (previously a redundant 2nd
      query before the spec lookup) was folded away entirely -- `getActivitySpec`'s own join
      already returns `undefined` for a missing attempt, so `findOrThrow` on its result covers
      both "attempt missing" and "spec missing" in one call, removing a query, not just
      relocating one.

      2 new integration tests (real Postgres, real publish pipeline, both the found and
      not-found cases). Updated 6 real test files' direct-instantiation call sites for the
      2 changed constructors (`ArtifactService`, `HintService`, `CommandExecutedConsumer`) plus 2
      module `providers` arrays. Full unit suite 142/142 passing, integration suite 74/74
      passing (72 previous + 2 new), `tsc --noEmit` clean. **Live-verified against the real
      running stack**: confirmed a clean NestJS boot with no DI/circularity errors ("Nest
      application successfully started"); `POST /v1/practice/attempts/{id}/tasks/t1/hints`
      against a real attempt returned a real hint (`HintService`'s migrated path); a real `nats
      pub` on `env.telemetry.COMMAND_EXECUTED` correctly wrote a real `COMMAND_EXECUTED` event
      (`CommandExecutedConsumer`'s migrated path); directly called the real compiled
      `ActivitySpecReader.getActivitySpec()` against both a real existing attempt (returned the
      real spec) and a nonexistent one (returned `undefined`, not a throw).~~
- [x] ~~**S6** — `publishedActivityVersionsQuery(qb, tenantId)`: new
      `practice-core/src/common/published-activity-versions-query.ts`. Found 4 real sites
      sharing the exact `content.activity_version as av` -> `content.activity as a` join,
      filtered by `a.tenant_id = ?` and `av.status = 'PUBLISHED'`
      (`catalog.repository.ts`'s `listPublishedCatalog`, `recommendation.service.ts` x2).
      `catalog.repository.ts`'s `listSkillDrivenCatalog` uses a related but genuinely different
      shape (LEFT JOIN with the checks as join conditions, not WHERE clauses, since an unmatched
      skill still needs to appear with null activity fields) -- correctly left as its own
      hand-written join, not forced through this function.

      **A real Kysely generic-typing problem worked through live, not guessed at**: a first
      attempt fixed the function's generic to the concrete `Database` type plus a `'av' | 'a'`
      table union -- compiled clean for the single-join call site, but produced real `tsc`
      errors on the 2 call sites that join additional tables (`content.activity_topic`/
      `content.topic`, `content.activity_skill`) before ever calling this function, since Kysely
      represents each `.selectFrom()`/`.innerJoin()` chain as its own distinct intersection type
      specific to that exact call site (confirmed via a deliberate type-mismatch probe reading
      the real inferred type off `tsc`'s own error output, not assumed) -- splitting the generic
      into separate `DB`/`TB`/`O` parameters could not preserve a caller's actual richer type
      through the function boundary, collapsing back to the function's own narrow declared
      constraint every time. Fixed by switching to Kysely's own documented `.$call<T>(func: (qb:
      this) => T): T` idiom -- one single generic `T` constrained structurally to just the two
      `.where()` overloads this function actually calls, which lets TypeScript infer `T` as the
      caller's exact, full type and thread it through untouched, with zero `as`/`as never`
      casts anywhere in the final version (an intermediate attempt used unsafe casts to force a
      compile; replaced once the structural-constraint approach was found to work cleanly).

      Migrated all 4 real call sites. Full unit suite 142/142 passing, integration suite 74/74
      passing unchanged (existing tests -- `recommendation.integration.spec.ts`'s "recommends
      remediation for a struggling skill," "excludes activities the learner has already
      attempted," etc -- already exercise both migrated `recommendation.service.ts` call sites
      with real, non-trivial assertions on the returned rows), `tsc --noEmit` clean. **Live-
      verified against the real running server**: `GET /v1/practice/activities`
      (`listPublishedCatalog`) returned all 64 real published activities, matching the known
      count from earlier in this session; `GET /v1/practice/recommendations` returned a real
      `200 OK` (empty for this specific learner's real history, a legitimate outcome, not an
      error); confirmed the compiled `dist/` output has the correct call-site counts (1 in
      `catalog.repository.js`, 2 in `recommendation.service.js`, matching source exactly).~~

## practice-core — Config/constants

- [x] ~~**K11** — `MasteryConstants`/`GrpcClientConstants`/`TimeoutConstants`: new
      `practice-core/src/common/constants.ts`. Confirmed the real scope via grep before
      extracting anything, not PLAN.md's named constants blindly: `REQUIRES_GATE_THRESHOLD`
      (`0.55`, doc §2.5 stage 2) was duplicated in 3 real places -- `mastery.service.ts`'s
      actual eligibility-gate check, plus 2 uses of the identical threshold in
      `recommendation.service.ts` (filtering to "struggling" skills, and the urgency-score
      formula). Deliberately did NOT fold in 2 other `0.55` literals that coincidentally share
      the value but are different concepts: `bkt.service.ts`'s mastery-band display boundary
      (Novice/Developing/Competent/... classification, unrelated to the eligibility gate) and
      `scoring-profile.ts`'s scoring weight -- checked each site's real meaning before deciding,
      not assumed identical because the number matched.

      `DEFAULT_DEADLINE_MS` (`30_000`) was duplicated in 2 places: `base-grpc-client.ts`'s
      `call()` default parameter and `grpc-orchestrator.client.ts`'s `execShell` (which adds a
      5s buffer on top). `WORKSPACE_FILE_OP_MS` (`10_000`) was duplicated in 3 places --
      `workspace-file.service.ts`'s list/read/write shell-exec calls, all independently
      hardcoding the same per-operation timeout.

      3 new tests confirming the extracted values match what was actually duplicated (not just
      that the constants exist). Migrated all 8 real call sites across 5 files
      (`mastery.service.ts`, `recommendation.service.ts` x2, `base-grpc-client.ts`,
      `grpc-orchestrator.client.ts`, `workspace-file.service.ts` x3). Full unit suite 145/145
      passing (142 previous + 3 new), integration suite 74/74 passing unchanged (confirms no
      numeric-value drift from the extraction), `tsc --noEmit` clean, confirmed zero leftover
      raw `0.55`/`30_000`/`10_000` sites for these specific concepts.

      **Live-verified against the real running server**: `POST /v1/practice/attempts` against
      the same activity/user combination checked earlier this session returned the identical
      real eligibility-gate rejection reason ("prerequisite mastery gate not met..."), confirming
      `MasteryConstants.REQUIRES_GATE_THRESHOLD` produces the same real behavior; confirmed the
      compiled `dist/` output has the correct call-site counts in all 3 affected files;
      `GET /v1/practice/attempts/{id}/tasks/t1/hints` (a real gRPC call through
      `BaseGrpcClient.call()`'s migrated deadline default) returned a real, correct hint preview.
      One honest gap: `TimeoutConstants.WORKSPACE_FILE_OP_MS` could not be exercised through a
      genuinely successful real `execShell` file-list call, since every currently-active real
      attempt in the database uses a `fake-env-*` stub environment (`FakeOrchestratorClient`),
      and the one attempted live call correctly reached the migrated code path (confirmed via
      the real server's own stack trace pointing exactly at `base-grpc-client.ts`'s `call()`)
      but failed for an unrelated, pre-existing reason (`NOT_FOUND: environment ... not found`
      -- a stale fake-environment record, not caused by this migration) -- noted explicitly
      rather than silently claimed as a full live check.~~

## Orchestrator — Config/constants

- [x] ~~**K14** — `internal/config.Config` struct: new `orchestrator/internal/config/config.go`.
      `main.go` read 18 real env vars (matching PLAN.md's "~15" estimate) via 6 ad-hoc
      `getEnv*`/`os.Getenv` calls scattered through its body, threading each one positionally
      into whichever of ~10 constructor calls needed it -- a real risk in a function this size,
      since a reordered parameter list at any call site silently compiles with values swapped,
      caught only by runtime misbehavior. One `config.Load()` call at the top of `main()`, one
      struct, named fields (`cfg.GRPCPort`, `cfg.DatabaseURL`, etc) at every one of the ~15
      downstream call sites instead of positional loose variables. `parseWarmPoolTargets`
      deliberately stays in `main.go`, not folded into `config` -- it calls
      `orchsvc.ImageForBlueprint`, and `config` importing the orchestrator service package would
      be a real import-direction inversion (`config` is meant to be a leaf package every other
      package can depend on), so `Config.WarmPoolTargets` stays the raw unparsed string and
      `main.go` parses it exactly as before.

      2 new tests, including one confirming every real fallback default matches what `main.go`
      previously had (pinned explicitly so a future edit can't silently drift a default without
      a test failure, same discipline as Phase 3's U7-U12) and one confirming real env vars
      actually get read through correctly, not just that defaults apply.

      Full orchestrator test suite passes across every package (`go test ./...`), `go vet
      ./...` clean, `gofmt -l .` clean (no formatting drift). **Live-verified against the real
      running stack, with explicit user confirmation before restarting the live process**:
      stopped the previously-running orchestrator (a stale build predating this change), started
      a freshly-built one, confirmed a clean real boot reading real, non-default `.env` values
      (not just defaults) -- log output showed the real recording bucket name/flush interval
      (`bucket=practice-recordings flush_interval=5s`) and real auth state (`gRPC authentication
      enabled`) correctly loaded from the real `.env` file; confirmed the gRPC server responds
      correctly via a real `grpcurl list` call with the real shared-secret bearer token; confirmed
      practice-core's real hint-preview endpoint still works correctly end-to-end against this
      freshly-restarted orchestrator (a real gRPC call through the full stack, not just a health
      check).~~
- [x] ~~**K15** — `k8s.DefaultT1Resources`/`DefaultT2Resources`: added to
      `orchestrator/internal/k8s/provision.go`. Confirmed the real scope via grep first, not
      every `resource.MustParse` hit in the codebase: T1's "2 CPU/4Gi" and T2's "8 CPU/16Gi"
      were independently hardcoded as bare string literals at 3 real sites --
      `limitRangeMaxFor`'s per-tier LimitRange ceiling, and the workspace pod's own container
      `Limits` fallback (T1 in `createWorkspacePod`, T2 in `applyT2PodShape`) -- plus a 4th,
      closely-related site: `resourceQuotaFor`'s per-tier namespace `requests.cpu`/
      `requests.memory` quota uses the identical numbers under a genuinely different K8s concept
      (namespace-wide request quota vs. per-container limit ceiling), but the coincidence
      reflects one real "how much does a T1/T2 environment get" decision, not two independent
      ones -- folded in too so the invariant is enforced, not just documented. Deliberately did
      NOT fold in `handlers_batch3.go`'s traffic-spike-fault Job resources (`10m/16Mi` request,
      `100m/32Mi` limit) -- checked that site's real meaning first and confirmed it's a
      specific fault-injection Job's own tiny sizing, an unrelated concept that happens to also
      call `resource.MustParse`.

      4 new tests, including 2 that assert `limitRangeMaxFor`/`resourceQuotaFor`'s real return
      values literally equal `DefaultT1Resources`/`DefaultT2Resources` (not just "happen to
      match today") -- these would fail immediately if either function's own literal ever
      drifted from the constants again, closing the actual gap, not just relocating the
      duplication. Existing tests (`TestApplyT2PodShape_DefaultsResourcesWhenUnset`,
      `TestApplyResourceQuota_T2HasHigherCeilingsThanT1`, `TestApplyLimitRange_
      T2HasHigherMaxThanT1`) continued passing unchanged, confirming no behavioral drift from
      the consolidation.

      Full orchestrator test suite passes (`go test ./...`), `go vet ./...` clean, `gofmt -l .`
      clean. **Live-verified against the real k3s cluster, with explicit user confirmation
      before restarting the orchestrator and provisioning real cluster resources**: restarted
      the orchestrator with the K15 build, provisioned a real T1 environment via a real
      `grpcurl Provision` call, then inspected the actual K8s objects via `kubectl` -- the real
      `ResourceQuota` showed `requests.cpu: 2, requests.memory: 4Gi` (via `resourceQuotaFor` ->
      `DefaultT1Resources`), the real `LimitRange` showed `max: {cpu: 2, memory: 4Gi}` (via
      `limitRangeMaxFor` -> `DefaultT1Resources`), and the real workspace pod's `shell`
      container showed `Limits: {cpu: 2, memory: 4Gi}` (via the migrated `nonEmpty(...,
      DefaultT1Resources.CPU)` fallback) -- all three independently-migrated call sites verified
      producing the identical real value from the one real cluster object graph, not just three
      separate unit-test assertions. Cleaned up the test environment via a real `Destroy` call
      afterward, confirmed the namespace correctly entered `Terminating` state.~~

---

## Verification gate (every item, before marking done)

- Functional: existing test suite still passes (`web`: `npm test`/vitest; `practice-core`:
  `npx jest` + integration suite; `orchestrator`: `go test ./...`) AND new tests cover the
  extracted abstraction in isolation.
- Security: no item in this phase is flagged security-adjacent in PLAN.md (unlike Phase 3's
  S8/K9), but any real call-site migration touching auth/ownership/tenant-scoping (S5, S6 both
  touch tenant-scoped queries) gets the same scrutiny as Phase 3's default.
- Live verification against the real running stack where the change is observable end-to-end
  (web dev server + practice-core :3001 + orchestrator :50051), matching this session's
  established standard — not just unit tests in isolation.
