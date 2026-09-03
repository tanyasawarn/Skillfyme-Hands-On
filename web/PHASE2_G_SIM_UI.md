# Phase 2 G — Simulation UI

The `/web` PRODUCTION_SIM surface. Fully implemented and tested (no
real-cluster dependency — it renders against the activity spec + the
attempt/evaluation the API already returns).

## What was built

| Requirement | Component | Where |
|---|---|---|
| **Ticket details** | `SimTicketPanel` — INC title + narrative from `meta.title`/`meta.summary`, difficulty badge, ref id + opened-at | `components/sim/SimShell.tsx` |
| **System symptoms** | `SymptomList` — one row per authored fault; T0 symptoms active from the start, T+N symptoms shown greyed with "escalates T+N" until their time | `components/sim/SimShell.tsx` |
| **SLA timer** | `SlaTimer` — live `mm:ss` elapsed vs `estimated_minutes` budget, colour ramps accent→warning→danger, "Over SLA" callout past budget | `components/sim/SimShell.tsx` |
| **Incident-note editor** | `IncidentNoteEditor` — 5-section markdown template, localStorage draft autosave, `POST /attempts/{id}/artifacts/{key}`, surfaces the "will be human-reviewed" provisional note | `components/sim/SimShell.tsx` |
| **Escalation (T+N)** | `EscalationBanner` — "heads up, escalates at T+N" while pending; "a second failure has surfaced — re-triage" once a T+N fault fires | `components/sim/SimShell.tsx` |
| **Debrief screen** | `SimDebrief` — (1) what was actually wrong, per fault; (2) the solution path from `objectives` + reference-solution location; (3) your run: score breakdown, non-zero penalties, per-task outcome with hint level | `components/sim/SimDebrief.tsx` |

Plus:
- `lib/sim.ts` — pure derivation (`buildTicket`, `buildSymptoms`,
  `computeClock`, `parseApplyAt`, `faultLabel`, `formatDuration`).
  **18 unit tests.**
- `components/attempt/HintPanel.tsx` — the hint UX extracted from the
  attempt page so the sim rail reuses it verbatim (no fork).
- `lib/api-client.ts` — `ActivitySpec` extended with the sim fields
  (`faults`, `artifacts_required`, `process_signals`,
  `reference_solution`, `meta.estimated_minutes`), plus `submitArtifact`.

## Wiring

`app/attempts/[id]/page.tsx` branches on `attempt.mode === 'PRODUCTION_SIM'`:
- workspace-active → `SimShell` instead of `TaskRail` (left rail widened 320→360px)
- evaluated → `SimDebrief` instead of `ResultPanel`

GUIDED_LAB attempts are untouched — same `TaskRail` + `ResultPanel`.

## Verification

| | |
|---|---|
| `npx tsc --noEmit` | clean |
| `npx eslint src/components/sim src/lib/sim.ts src/components/attempt` | clean |
| `npx vitest run` | **125 passed** (95 pre-existing + 18 sim-lib + 7 SimShell + 5 SimDebrief) |
| `npx next build` | compiles, all routes build |

(One pre-existing eslint error in `components/ui/EmptyState.spec.tsx` —
not touched by this work.)

## Not in scope / follow-ups

- **Richer per-fault explanations in the debrief.** `SimDebrief` takes a
  `faultNotes` prop (fault id → text) for when a future endpoint exposes
  the fault content's `canonical_diagnostic_path` / human explanation;
  today it renders fault id + de-kebabbed label + a generic line.
- **Live `T+N` fault injection is orchestrator-side.** The UI reflects
  the escalation clock from `apply_at`; the orchestrator's fault engine
  is what actually injects at T+N (requirement B). The two are
  independent — the UI would show "escalated" on schedule even if the
  backend injected slightly early/late; acceptable.
- **Server-driven symptom feed.** Symptoms are derived from the authored
  fault list, not a live "what's currently broken" query. Sufficient for
  the sim's purpose (the learner is meant to discover the symptoms
  themselves); a live feed would give it away.
