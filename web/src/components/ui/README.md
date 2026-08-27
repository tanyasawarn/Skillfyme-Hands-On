# `components/ui/`

Shared, presentation-only components. If you're about to hand-roll loading spinners, status
pills, empty-state copy, or a page wrapper inside a route file, check here first — it probably
already exists.

## The rule

`components/ui/` holds **presentation-only, prop-driven components with no data-fetching**.
A component here never calls `useQuery`/`api.*` itself; it receives data and callbacks as props
and renders. Page-level data fetching stays in `src/app/**/page.tsx`; page-specific markup that
isn't reused anywhere else stays in that page's own file, not here.

This directory (and the equivalent rule in `practice-core/src/common/`) exists because an earlier
audit of this codebase found 119 duplicated/hardcoded patterns across the project — a direct
consequence of nobody having a designated place to put shared UI code, so every contributor
re-solved the same problem locally instead of reusing an existing solution. Two of those
duplications had already caused live, shipped bugs by the time the audit ran. Putting something
here isn't optional tidiness — it's what keeps the next person from writing a 4th slightly-
different version of a badge, a loading state, or a card.

## What's here today

| Component | Purpose |
|---|---|
| `Alert.tsx` | Inline error/warning banner |
| `Badge.tsx` | Status/mastery/difficulty pill — one `variant` prop covers every color state used across the app; also supports a circle-icon shape for task status |
| `Button.tsx` | The shared button, wraps the `.lms-action-btn` CSS system |
| `CardLink.tsx` | Clickable-card chrome (border/radius/background/hover) in 3 variants (`lift`/`row`/`plain`) plus a disabled non-link state — owns the chrome only, callers still compose their own inner content |
| `EmptyState.tsx` | "Nothing here yet" messaging, in a plain-message or bordered-card shape |
| `Loader.tsx` | Loading spinner + label |
| `PageContainer.tsx` | The `mx-auto max-w-* px-6 py-*` route wrapper, with configurable `maxWidth`/`spacing` |
| `ProgressBar.tsx` | 0–1 fraction progress/mastery bar, configurable fill color and animation |
| `QueryBoundary.tsx` | Wraps the `isLoading`/`isError` inline-rendering shape (loading spinner or error banner alongside a page's own header) — for the specific "chrome stays up, content is pending" pattern; pages using an early-return "nothing to show yet" shape instead don't use this |
| `SectionLabel.tsx` | Wraps the `.font-mono-label` CSS class; polymorphic `as` prop (`h2`/`h3`/`p`/`span`/`label`) so heading level isn't picked ad hoc per call site |
| `WithSearchParamsSuspense.tsx` | Suspense boundary for a page that reads `useSearchParams()`, with a consistent loading fallback |

Each component has a matching `.spec.tsx` in this same directory — if you add a component here,
add its test alongside it, same as every existing one.

## Where the pure functions/constants live

Non-component shared code (formatters, route builders, constants, data hooks) lives in
`src/lib/`, not here — this directory is components only.
