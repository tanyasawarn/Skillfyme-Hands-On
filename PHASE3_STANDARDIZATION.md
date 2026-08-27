# Phase 3 — Development, Standardization & Hardening

Tracks the Phase 3 checklist against this repo's real state. A codebase survey ran first
(see survey findings folded into each item below) since several items were already partially
or fully built before this pass started — items are marked complete only where real code,
tests, and live verification back the claim.

Section 3 ("Feature Implementation") in the original checklist was a generic template
("replace with your actual feature list if needed") — this repo's real features (guided labs,
fault injection, AI grading, environment orchestration) already exist and are tracked in
`PHASE1_REMEDIATION.md` / `PHASE2_CLOSEOUT.md`, not duplicated here.

---

## 1. Global Architecture & Reusability Layer (Frontend)

- [x] ~~Global Component Library — `Button`/`Alert`/`Loader` in `web/src/components/ui/`,
      replacing 4+ inline-duplicated `<button className="lms-action-btn...">` call sites and
      2 near-verbatim-duplicated error banners (`catalog/page.tsx`, `history/page.tsx`). Wraps
      the pre-existing `.lms-action-btn` CSS class system (already solid) rather than
      reinventing styling. 20 new Vitest + React Testing Library tests, all passing~~
- [x] ~~Design Token System — already existed and is solid: CSS custom properties in
      `globals.css` (color/spacing/typography), Tailwind v4 CSS-first config, consistently
      referenced via `var(--token)` throughout. No work needed~~
- [x] ~~Global API Service Layer — already existed and is solid: `web/src/lib/api-client.ts`,
      one typed `request<T>()` wrapper, zero raw `fetch()` bypasses anywhere in the app
      (confirmed by grep). No work needed~~
- [x] ~~Error Handling Module — `web/src/lib/error-message.ts`'s `toUserFacingError()`,
      replacing the duplicated "assume unreachable API" assumption in both list pages and the
      ad-hoc `ApiError` check in `WorkspaceEditor.tsx`'s save handler. Distinguishes a real API
      error (shows the backend's now-meaningful message) from a genuine network failure from
      an unexpected bug. 6 new tests~~
- [ ] Reusable Form Engine — correctly NOT built: survey found no `zod`/`react-hook-form` in
      `web/`, and there are effectively no free-text forms in the app today (it's click-driven).
      Building this speculatively ahead of a real form requirement would be guessing at an API
      shape with nothing to validate it against
- [ ] Auth & Session Module (frontend) — correctly NOT built: `web/src/lib/auth-token.ts`
      already has real token caching + proactive refresh; the missing piece (a real learner
      identity/session layer, not the hardcoded `DEMO_USER_ID`) is explicitly documented in
      `demo-context.ts`/`ProfileMenu.tsx` as a Phase-1 stub awaiting a real login flow that
      doesn't exist yet. Backend already has the real piece (`AuthGuard`/`RolesGuard`, global)
- [x] ~~Global Config System — already existed: `NEXT_PUBLIC_API_BASE_URL` read consistently in
      both `api-client.ts` and `auth-token.ts` with the same documented dev-only fallback. No
      hardcoded URLs found elsewhere in `web/src`~~

## 2. Backend Standardization

- [x] ~~Service Layer Architecture — already existed and is clean: controllers spot-checked
      (`attempt.controller.ts`, `catalog.controller.ts`) are thin, delegate to injected
      services/repositories. No work needed~~
- [x] ~~Global Exception Handling — `practice-core/src/common/all-exceptions.filter.ts`
      (`AllExceptionsFilter`), wired via `app.useGlobalFilters()` in `main.ts`. Closes a real
      gap: 21 `executeTakeFirstOrThrow()` call sites across `src/modules/` previously threw raw
      Kysely errors that fell through to Nest's default handler with no guarantee against
      leaking DB error detail to the client -- confirmed live: a malformed-UUID request that
      triggers a real Postgres error now returns exactly `{"statusCode":500,"message":"Internal
      server error"}`, no leaked detail, while HttpExceptions (404/403/etc, already used
      consistently throughout) pass through with their real message in a consistent envelope.
      8 new unit tests~~
- [ ] Common Utility Modules — MISSING, not yet built: no shared date-utility module; date math
      hand-rolled independently in 15 files. Backlogged (lower priority than items 1/3/4, per
      the priority order this pass followed) -- cheap to extract when next touching that code,
      not urgent enough to justify a speculative refactor pass across 15 files right now
- [x] ~~Centralized Logging — already existed and is solid: NestJS `Logger` used consistently
      across 12 files, zero `console.log` in production code. No work needed~~

## 3. Feature Implementation

Out of scope for this tracker -- see note above. This repo's real features already exist and
are tracked in `PHASE1_REMEDIATION.md` / `PHASE2_CLOSEOUT.md`.

## 4. Hardcoding Audit & Refactor

- [x] ~~Frontend: no hardcoded URLs found beyond one documented dev-only fallback (duplicated
      in 2 files, both intentional). Backend: one documented dev-only CORS origin fallback
      (`main.ts`); `evaluation/scoring-profile.ts`'s numeric literals are named fields on a
      typed config object citing a design-doc source, not unexplained magic numbers -- a
      defensible pattern, not a violation. `zod` is an installed-but-unused dependency in
      practice-core (no schema validation actually wired up despite being present) -- flagged,
      not removed or wired up this pass (removing an unused dep or building a validation layer
      around it are both separate scope decisions, not a "hardcoding" fix)~~

## 5 + 6. Testing Layer & Security Testing

- [x] ~~Frontend test infrastructure — did not exist at all before this pass (no Jest/Vitest,
      zero test files). Set up Vitest + React Testing Library + jsdom (explicit user choice
      over skipping or manual-only verification), `npm run test` script, 20 tests across the
      new component library + error-message module, plus a full `next build` production build
      verified clean~~
- [x] ~~XSS/CSRF protection — helmet added to practice-core (`main.ts`), CSP module explicitly
      disabled with a documented reason (pure JSON API, no HTML rendering, CSP doesn't apply;
      a default CSP would be dead weight not real protection). Live-verified: `X-Content-
      Type-Options`, `X-Frame-Options`, `Cross-Origin-Resource-Policy`, `Referrer-Policy`
      present on real responses, `X-Powered-By: Express` no longer leaked. Next.js security
      headers added (`next.config.ts`'s `headers()`): nosniff, frame-options, referrer-policy,
      permissions-policy -- deliberately NOT a full CSP yet, since this app embeds Monaco +
      xterm.js (both use web workers/eval-adjacent code paths a wrong CSP would silently break
      without obvious failure signs); live-verified on a real built-and-served page.
      CSRF: structurally lower risk already (bearer JWT in Authorization header, not
      cookie-based sessions -- no ambient credential for a CSRF request to exploit), not
      separately mitigated beyond that architectural fact~~
- [x] ~~Dependency vulnerability scan — practice-core: 0 vulnerabilities (before and after this
      pass). web/: found 2 real vulnerabilities (1 moderate, 1 low) -- DOMPurify <=3.4.12 via
      the transitive `monaco-editor` dependency, 4 real CVEs including an XSS bypass, directly
      relevant given this exact library sanitizes editor content. `npm audit fix` alone
      couldn't resolve it (the advisory's suggested fix targets a monaco-editor prerelease
      build); fixed via an `overrides` entry pinning dompurify to `^3.4.14` (patched).
      Live-verified: `npm ls dompurify` confirms 3.4.14 installed, `npm audit` now reports 0
      vulnerabilities, full typecheck/test/production-build all still pass with the override
      in place (Monaco itself unaffected)~~
- [x] ~~Rate limiting — MISSING before this pass, now added: `@nestjs/throttler`, global
      default (100 req/60s per client IP) via `ThrottlerGuard` registered as an `APP_GUARD`
      BEFORE `AuthGuard` (ordering matters -- throttling must apply to unauthenticated/
      pre-auth traffic too, since the highest-value target, `/v1/auth/dev-login`, is itself
      `@Public()`). Tighter override on `dev-login` specifically (5 req/60s) -- that endpoint
      mints a real, valid 12h JWT on every call with no password check, making it the single
      highest-value target for a naive automated-abuse attempt. Live-verified: 7 rapid calls to
      dev-login → first 5 succeed (201), 6th/7th correctly rejected with 429 + `Retry-After`
      header, response body clean (no leaked internals, routed through the same exception
      filter as everything else); a normal endpoint under the 100/60s default limit confirmed
      unaffected by a small burst. Full unit (115) + integration (72) suites still pass~~

## 7. Performance Optimization

- [ ] Not yet started this pass

## 8. Final QA & Stability

- [ ] Not yet started this pass
