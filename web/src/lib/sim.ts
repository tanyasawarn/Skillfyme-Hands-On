/**
 * PRODUCTION_SIM presentation model (doc §1.3.2, §3.3). The activity
 * spec doesn't carry an explicit "ticket" / "symptoms" / "escalation"
 * shape — the simulation UI derives them from what the spec DOES carry
 * (`meta`, `faults[].apply_at`, `estimated_minutes`, `process_signals`)
 * plus the attempt's own `started_at`. Keeping that derivation here,
 * pure and tested, so the components stay dumb.
 */
import type { ActivitySpec, Attempt } from './api-client';

export interface SimTicket {
  /** INC-style title from meta.title, or a fallback. */
  title: string;
  /** the incident narrative — meta.summary. */
  body: string;
  difficulty: string | null;
}

export interface SimSymptom {
  /** short label, derived from the fault id's last segment. */
  label: string;
  /** the fault id, for the debrief cross-reference. */
  faultId: string;
  /** "T0" (present from the start) or "T+12" (escalates at minute 12). */
  appliesAt: string;
  /** minutes after start this becomes active; 0 for T0. */
  appliesAtMinutes: number;
}

export interface SimClock {
  /** wall-clock ms elapsed since the attempt started. */
  elapsedMs: number;
  /** the SLA budget in ms (estimated_minutes × 60_000), or null if unset. */
  budgetMs: number | null;
  /** elapsedMs / budgetMs, clamped [0, 1.5] so the bar can show overrun. */
  fraction: number;
  /** true once elapsed exceeds the budget. */
  overSla: boolean;
  /** the next not-yet-fired escalation, or null if none remain. */
  nextEscalation: SimSymptom | null;
  /** escalations whose T+N has already elapsed. */
  firedEscalations: SimSymptom[];
}

/** "T0" -> 0 ; "T+12" -> 12 ; anything else -> 0. */
export function parseApplyAt(applyAt: string): number {
  const m = /^T\+(\d+)$/.exec(applyAt.trim());
  return m ? Number(m[1]) : 0;
}

export function buildTicket(spec: ActivitySpec): SimTicket {
  return {
    title: spec.meta?.title?.trim() || 'Production incident',
    body:
      spec.meta?.summary?.trim() ||
      'An incident has been raised against this service. Diagnose the root cause(s) and restore normal operation.',
    difficulty: spec.meta?.difficulty_level ?? null,
  };
}

/** One symptom per authored fault, sorted T0 first then by escalation time. */
export function buildSymptoms(spec: ActivitySpec): SimSymptom[] {
  const faults = spec.faults ?? [];
  return faults
    .map((f) => {
      const appliesAtMinutes = parseApplyAt(f.apply_at);
      return {
        faultId: f.id,
        appliesAt: f.apply_at,
        appliesAtMinutes,
        label: faultLabel(f.id),
      };
    })
    .sort((a, b) => a.appliesAtMinutes - b.appliesAtMinutes);
}

/** "f.k8s.wrong-service-selector" -> "wrong service selector". */
export function faultLabel(faultId: string): string {
  const parts = faultId.split('.');
  const tail = parts[parts.length - 1] ?? faultId;
  return tail.replace(/-/g, ' ');
}

export function computeClock(
  spec: ActivitySpec,
  attempt: Pick<Attempt, 'started_at'>,
  now: number = Date.now(),
): SimClock {
  const startedAt = attempt.started_at ? Date.parse(attempt.started_at) : now;
  const elapsedMs = Math.max(0, now - startedAt);

  const estMinutes = spec.meta?.estimated_minutes ?? null;
  const budgetMs = estMinutes != null ? estMinutes * 60_000 : null;

  const fraction =
    budgetMs && budgetMs > 0 ? Math.min(1.5, elapsedMs / budgetMs) : 0;
  const overSla = budgetMs != null && elapsedMs > budgetMs;

  const escalations = buildSymptoms(spec).filter((s) => s.appliesAtMinutes > 0);
  const elapsedMinutes = elapsedMs / 60_000;
  const fired = escalations.filter((s) => elapsedMinutes >= s.appliesAtMinutes);
  const pending = escalations.filter((s) => elapsedMinutes < s.appliesAtMinutes);

  return {
    elapsedMs,
    budgetMs,
    fraction,
    overSla,
    nextEscalation: pending[0] ?? null,
    firedEscalations: fired,
  };
}

/** mm:ss for a duration in ms. */
export function formatDuration(ms: number): string {
  const total = Math.floor(ms / 1000);
  const m = Math.floor(total / 60);
  const s = total % 60;
  return `${String(m).padStart(2, '0')}:${String(s).padStart(2, '0')}`;
}

export function isProductionSim(mode: string | undefined | null): boolean {
  return mode === 'PRODUCTION_SIM';
}
