'use client';

/**
 * PRODUCTION_SIM left-rail shell (doc §1.3.2, §8.5). Replaces the plain
 * GUIDED_LAB TaskRail for `mode: PRODUCTION_SIM` attempts with the
 * incident-response surface the doc's simulation flow calls for:
 *
 *   - the TICKET (INC title + narrative)                — SimTicketPanel
 *   - the SLA TIMER (elapsed vs estimated budget)       — SlaTimer
 *   - SYSTEM SYMPTOMS, incl. ones that escalate at T+N  — SymptomList
 *   - an ESCALATION banner when a T+N fault has fired   — EscalationBanner
 *   - the tasks (same shape as GUIDED_LAB)             — reuses task rows
 *   - the INCIDENT-NOTE editor (required artifact)      — IncidentNoteEditor
 *
 * The debrief screen (post-submit) is SimDebrief, rendered by the
 * attempt page once the attempt is evaluated.
 */
import { useEffect, useRef, useState } from 'react';
import { useMutation } from '@tanstack/react-query';
import type { ActivitySpec, Attempt } from '@/lib/api-client';
import { api } from '@/lib/api-client';
import {
  buildTicket,
  buildSymptoms,
  computeClock,
  formatDuration,
  faultLabel,
  type SimClock,
} from '@/lib/sim';
import { toUserFacingError } from '@/lib/error-message';
import { Badge } from '@/components/ui/Badge';
import { Button } from '@/components/ui/Button';
import { SectionLabel } from '@/components/ui/SectionLabel';
import { HintPanel } from '@/components/attempt/HintPanel';

interface SimShellProps {
  attemptId: string;
  attempt: Attempt;
  spec: ActivitySpec;
}

export function SimShell({ attemptId, attempt, spec }: SimShellProps) {
  const ticket = buildTicket(spec);
  const symptoms = buildSymptoms(spec);
  const tasks = spec.tasks ?? [];
  const noteArtifact = (spec.artifacts_required ?? []).find((a) => a.type === 'MARKDOWN');

  // Live 1s clock while the attempt is in progress.
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const t = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(t);
  }, []);
  const clock = computeClock(spec, attempt, now);

  return (
    <div className="lms-card flex min-h-0 flex-col overflow-hidden">
      <div className="min-h-0 flex-1 overflow-y-auto">
        <SlaTimer clock={clock} />
        {clock.nextEscalation || clock.firedEscalations.length > 0 ? (
          <EscalationBanner clock={clock} />
        ) : null}

        <div className="border-b border-[var(--border)] px-4 py-3">
          <SimTicketPanel ticket={ticket} attempt={attempt} />
        </div>

        {symptoms.length > 0 && (
          <div className="border-b border-[var(--border)] px-4 py-3">
            <SymptomList symptoms={symptoms} clock={clock} />
          </div>
        )}

        <div className="px-4 py-3">
          <SectionLabel>Tasks</SectionLabel>
          <div className="mt-2">
            {tasks.length === 0 ? (
              <p className="text-xs text-[var(--ink-soft)]">No tasks for this simulation.</p>
            ) : (
              tasks.map((task, i) => (
                <div key={task.key} className={i > 0 ? 'lms-divider-dashed mt-4 pt-4' : ''}>
                  <div className="flex items-start justify-between gap-2">
                    <p className="text-sm font-semibold text-[var(--ink)]">{task.title}</p>
                    {task.required && (
                      <span className="shrink-0 font-mono text-[9px] uppercase tracking-wide text-[var(--ink-soft)]">
                        required
                      </span>
                    )}
                  </div>
                  <p className="mt-1.5 whitespace-pre-wrap text-xs leading-relaxed text-[var(--ink-muted)]">
                    {task.instructions_md}
                  </p>
                  <HintPanel attemptId={attemptId} taskKey={task.key} />
                </div>
              ))
            )}
          </div>
        </div>

        {noteArtifact && (
          <div className="border-t border-[var(--border)] px-4 py-3">
            <IncidentNoteEditor attemptId={attemptId} artifactKey={noteArtifact.key} rubricId={noteArtifact.rubric} />
          </div>
        )}
      </div>
    </div>
  );
}

// ---------------------------------------------------------------------------

function SimTicketPanel({ ticket, attempt }: { ticket: ReturnType<typeof buildTicket>; attempt: Attempt }) {
  return (
    <div>
      <div className="flex items-center gap-2">
        <SectionLabel as="p">Incident ticket</SectionLabel>
        {ticket.difficulty && <Badge variant="muted">{ticket.difficulty}</Badge>}
      </div>
      <p className="mt-1.5 text-sm font-semibold text-[var(--ink)]">{ticket.title}</p>
      <p className="mt-1.5 whitespace-pre-wrap text-xs leading-relaxed text-[var(--ink-muted)]">{ticket.body}</p>
      <p className="mt-2 font-mono text-[10px] text-[var(--ink-soft)]">
        ref {attempt.id.slice(0, 8)} · opened{' '}
        {attempt.started_at ? new Date(attempt.started_at).toLocaleTimeString() : '—'}
      </p>
    </div>
  );
}

// ---------------------------------------------------------------------------

function SlaTimer({ clock }: { clock: SimClock }) {
  const pct = Math.round(clock.fraction * 100);
  const barColor = clock.overSla
    ? 'bg-[var(--danger)]'
    : clock.fraction > 0.8
      ? 'bg-[var(--warning)]'
      : 'bg-[var(--accent)]';
  return (
    <div className="border-b border-[var(--border)] px-4 py-3">
      <div className="flex items-baseline justify-between">
        <SectionLabel as="p">SLA timer</SectionLabel>
        <span
          className={`font-mono text-sm tabular-nums ${clock.overSla ? 'text-[var(--danger)]' : 'text-[var(--ink)]'}`}
        >
          {formatDuration(clock.elapsedMs)}
          {clock.budgetMs != null && (
            <span className="text-[var(--ink-soft)]"> / {formatDuration(clock.budgetMs)}</span>
          )}
        </span>
      </div>
      {clock.budgetMs != null && (
        <div className="mt-2 h-1.5 overflow-hidden rounded-full bg-[var(--border)]">
          <div className={`h-full ${barColor}`} style={{ width: `${Math.min(100, pct)}%` }} />
        </div>
      )}
      {clock.overSla && (
        <p className="mt-1.5 text-[11px] font-semibold text-[var(--danger)]">
          Over SLA — every extra minute counts against the overtime penalty.
        </p>
      )}
    </div>
  );
}

// ---------------------------------------------------------------------------

function SymptomList({ symptoms, clock }: { symptoms: ReturnType<typeof buildSymptoms>; clock: SimClock }) {
  const firedIds = new Set(clock.firedEscalations.map((s) => s.faultId));
  return (
    <div>
      <SectionLabel as="p">System symptoms</SectionLabel>
      <ul className="mt-2 space-y-1.5">
        {symptoms.map((s) => {
          const isEscalation = s.appliesAtMinutes > 0;
          const active = !isEscalation || firedIds.has(s.faultId);
          return (
            <li key={s.faultId} className="flex items-center gap-2 text-xs">
              <span
                aria-hidden
                className={`inline-block h-1.5 w-1.5 shrink-0 rounded-full ${
                  active ? 'bg-[var(--danger)]' : 'bg-[var(--border)]'
                }`}
              />
              <span className={active ? 'text-[var(--ink)]' : 'text-[var(--ink-soft)]'}>{s.label}</span>
              {isEscalation && (
                <span className="ml-auto font-mono text-[10px] text-[var(--ink-soft)]">
                  {active ? 'escalated' : `escalates ${s.appliesAt}`}
                </span>
              )}
            </li>
          );
        })}
      </ul>
    </div>
  );
}

// ---------------------------------------------------------------------------

function EscalationBanner({ clock }: { clock: SimClock }) {
  const justFired = clock.firedEscalations[clock.firedEscalations.length - 1];
  if (justFired) {
    return (
      <div className="border-b border-[var(--danger)] bg-[var(--danger-soft)] px-4 py-2 text-xs text-[var(--danger)]">
        <span className="font-semibold">Escalation:</span> a second failure —{' '}
        <span className="font-mono">{faultLabel(justFired.faultId)}</span> — has now surfaced ({justFired.appliesAt}).
        Re-triage; the ticket now has more than one root cause.
      </div>
    );
  }
  if (clock.nextEscalation) {
    return (
      <div className="border-b border-[var(--warning)] bg-[var(--warning-soft)] px-4 py-2 text-xs text-[var(--warning)]">
        <span className="font-semibold">Heads up:</span> this incident is expected to escalate at{' '}
        <span className="font-mono">{clock.nextEscalation.appliesAt}</span> if not contained.
      </div>
    );
  }
  return null;
}

// ---------------------------------------------------------------------------

function IncidentNoteEditor({
  attemptId,
  artifactKey,
  rubricId,
}: {
  attemptId: string;
  artifactKey: string;
  rubricId?: string;
}) {
  const draftKey = `incident-note:${attemptId}:${artifactKey}`;
  const [content, setContent] = useState(() => {
    try {
      return localStorage.getItem(draftKey) ?? TEMPLATE;
    } catch {
      return TEMPLATE;
    }
  });
  const [savedNote, setSavedNote] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const debounce = useRef<ReturnType<typeof setTimeout> | null>(null);

  const onChange = (v: string) => {
    setContent(v);
    if (debounce.current) clearTimeout(debounce.current);
    debounce.current = setTimeout(() => {
      try {
        localStorage.setItem(draftKey, v);
      } catch {
        /* private window / storage disabled — the in-memory draft still holds */
      }
    }, 400);
  };

  const submitMutation = useMutation({
    mutationFn: () => api.submitArtifact(attemptId, artifactKey, content),
    onSuccess: (res) => {
      setError(null);
      setSavedNote(
        res.provisional
          ? 'Submitted. This note will be human-reviewed before it affects any score.'
          : 'Submitted.',
      );
    },
    onError: (err) => setError(toUserFacingError(err).headline),
  });

  return (
    <div>
      <div className="flex items-center gap-2">
        <SectionLabel as="p">Incident note</SectionLabel>
        {rubricId && <span className="font-mono text-[10px] text-[var(--ink-soft)]">{rubricId}</span>}
      </div>
      <p className="mt-1 text-[11px] text-[var(--ink-muted)]">
        Root cause, detection, timeline, remediation, prevention. Saved as you type; submit before you finish the
        attempt.
      </p>
      <textarea
        value={content}
        onChange={(e) => onChange(e.target.value)}
        spellCheck
        className="lms-inset-field mt-2 h-56 w-full resize-y rounded-md p-2.5 font-mono text-xs leading-relaxed text-[var(--ink)]"
        aria-label="Incident note editor"
      />
      <div className="mt-2 flex items-center gap-3">
        <Button
          size="sm"
          onClick={() => submitMutation.mutate()}
          disabled={submitMutation.isPending || content.trim().length < 40}
        >
          {submitMutation.isPending ? 'Submitting…' : 'Submit incident note'}
        </Button>
        {content.trim().length < 40 && (
          <span className="text-[11px] text-[var(--ink-soft)]">Write a bit more before submitting.</span>
        )}
      </div>
      {savedNote && <p className="mt-1.5 text-[11px] text-[var(--success)]">{savedNote}</p>}
      {error && <p className="mt-1.5 text-[11px] text-[var(--danger)]">Couldn&apos;t submit: {error}</p>}
    </div>
  );
}

const TEMPLATE = `## Root cause

## Detection

## Timeline

## Remediation

## Prevention
`;

