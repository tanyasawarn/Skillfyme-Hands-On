'use client';

/**
 * PRODUCTION_SIM debrief screen (doc §1.3.2 step 10, §7.5). Shown by the
 * attempt page once a SIM attempt is evaluated. Three sections the doc's
 * debrief calls for:
 *
 *   1. Fault explanation — what was actually injected, per fault.
 *   2. Correct solution path — the canonical diagnostic steps + where
 *      the reference solution lives (once it's visible).
 *   3. Learner activity timeline — the deterministic score breakdown,
 *      process-signal penalties, and per-task outcome, in order.
 *
 * The fault content (canonical diagnostic path, human explanation) isn't
 * in the activity spec the web app already fetches, so this renders what
 * IS available: the fault ids + labels from the spec, the evaluation
 * breakdown, penalties, and task states. A `faultNotes` prop lets the
 * page pass richer per-fault text if a future endpoint exposes it.
 */
import type {
  ActivitySpec,
  AttemptEvaluation,
  AttemptTaskState,
} from '@/lib/api-client';
import { buildSymptoms } from '@/lib/sim';
import { formatPercent } from '@/lib/format';
import { Badge } from '@/components/ui/Badge';
import { SectionLabel } from '@/components/ui/SectionLabel';
import { ProgressBar } from '@/components/ui/ProgressBar';

interface SimDebriefProps {
  spec: ActivitySpec;
  evaluation: AttemptEvaluation;
  tasks?: AttemptTaskState[];
  /** optional richer per-fault explanation, keyed by fault id */
  faultNotes?: Record<string, string>;
}

export function SimDebrief({ spec, evaluation, tasks, faultNotes }: SimDebriefProps) {
  const symptoms = buildSymptoms(spec);
  const penalties = Object.entries(evaluation.penalties_jsonb ?? {}).filter(([, v]) => v > 0);
  const solutionPath = spec.reference_solution?.repo_path;

  return (
    <div className="lms-card mt-6 overflow-hidden">
      <div
        className={`px-6 py-5 ${
          evaluation.passed ? 'bg-[var(--success-soft)]' : 'bg-[var(--danger-soft)]'
        }`}
      >
        <SectionLabel as="p">Incident debrief</SectionLabel>
        <p className="mt-0.5 text-xs text-[var(--ink-soft)]">
          {evaluation.passed ? 'Resolved' : 'Not resolved'} · final score{' '}
          {formatPercent(Number(evaluation.final_score))} · {evaluation.profile_version_id}
        </p>
      </div>

      <div className="space-y-6 p-6">
        {/* 1. Fault explanation */}
        <section>
          <SectionLabel as="h3">What was actually wrong</SectionLabel>
          <ul className="mt-3 space-y-3">
            {symptoms.map((s) => (
              <li key={s.faultId} className="lms-inset-field p-3">
                <div className="flex items-center gap-2">
                  <span className="text-sm font-semibold capitalize text-[var(--ink)]">{s.label}</span>
                  <span className="font-mono text-[10px] text-[var(--ink-soft)]">{s.faultId}</span>
                  {s.appliesAtMinutes > 0 && <Badge variant="warning">escalated {s.appliesAt}</Badge>}
                </div>
                <p className="mt-1.5 text-xs leading-relaxed text-[var(--ink-muted)]">
                  {faultNotes?.[s.faultId] ??
                    'This fault was injected into your environment after the health gate passed. See the reference solution for the full diagnostic path and fix.'}
                </p>
              </li>
            ))}
            {symptoms.length === 0 && (
              <li className="text-xs text-[var(--ink-soft)]">No faults recorded for this simulation.</li>
            )}
          </ul>
        </section>

        {/* 2. Correct solution path */}
        <section className="lms-divider-dashed pt-5">
          <SectionLabel as="h3">The solution path</SectionLabel>
          {spec.objectives && spec.objectives.length > 0 ? (
            <ol className="mt-3 list-decimal space-y-1.5 pl-5 text-xs text-[var(--ink-muted)]">
              {spec.objectives.map((o, i) => (
                <li key={i}>{o}</li>
              ))}
            </ol>
          ) : (
            <p className="mt-2 text-xs text-[var(--ink-soft)]">No objectives listed.</p>
          )}
          {solutionPath && (
            <p className="mt-3 text-xs text-[var(--ink-muted)]">
              Reference solution:{' '}
              <span className="font-mono text-[var(--ink-soft)]">{solutionPath}</span>
              {spec.reference_solution?.visibility && (
                <span className="text-[var(--ink-soft)]"> ({spec.reference_solution.visibility})</span>
              )}
            </p>
          )}
        </section>

        {/* 3. Learner activity timeline */}
        <section className="lms-divider-dashed pt-5">
          <SectionLabel as="h3">Your run</SectionLabel>

          <div className="mt-3 space-y-3">
            {Object.entries(evaluation.breakdown_jsonb).map(([key, crit]) => (
              <div key={key} className="flex items-center gap-3 text-sm">
                <span className="w-44 shrink-0 capitalize text-[var(--ink-muted)]">
                  {key.replace(/_/g, ' ')}
                </span>
                <ProgressBar value={crit.value} fillClassName="bg-[var(--accent)]" animated />
                <span className="w-11 shrink-0 text-right font-mono text-xs tabular-nums text-[var(--ink-soft)]">
                  {formatPercent(crit.value)}
                </span>
              </div>
            ))}
          </div>

          {penalties.length > 0 && (
            <div className="mt-4">
              <p className="font-mono text-[10px] uppercase tracking-wide text-[var(--ink-soft)]">Penalties</p>
              <ul className="mt-1.5 space-y-1 text-xs text-[var(--danger)]">
                {penalties.map(([k, v]) => (
                  <li key={k}>
                    <span className="capitalize">{k.replace(/_/g, ' ')}</span> −{formatPercent(v)}
                  </li>
                ))}
              </ul>
            </div>
          )}

          {tasks && tasks.length > 0 && (
            <div className="mt-4">
              <p className="font-mono text-[10px] uppercase tracking-wide text-[var(--ink-soft)]">Tasks</p>
              <ul className="mt-1.5 space-y-1.5">
                {tasks.map((t) => (
                  <li key={t.task_key} className="flex items-center gap-2.5 text-sm">
                    <TaskDot status={t.status} />
                    <span className="text-[var(--ink)]">{t.task_key}</span>
                    <span className="text-xs capitalize text-[var(--ink-soft)]">
                      {t.status.toLowerCase()}
                      {t.hints_used_max_level > 0 && ` · hint L${t.hints_used_max_level}`}
                    </span>
                  </li>
                ))}
              </ul>
            </div>
          )}
        </section>
      </div>
    </div>
  );
}

function TaskDot({ status }: { status: string }) {
  if (status === 'PASSED') return <Badge shape="circle" variant="success">✓</Badge>;
  if (status === 'FAILED') return <Badge shape="circle" variant="danger">✕</Badge>;
  return <Badge shape="circle" variant="muted">·</Badge>;
}

