'use client';

import { use, useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import type { AttemptStatus, HintReveal } from '@/lib/api-client';
import { api } from '@/lib/api-client';
import { WorkspaceTerminal } from '@/components/WorkspaceTerminal';
import { WorkspaceEditor } from '@/components/WorkspaceEditor';

const STATUS_COLOR: Record<AttemptStatus, string> = {
  CREATED: 'lms-pill--muted',
  PROVISIONING: 'lms-pill--warning',
  READY: 'lms-pill--accent',
  IN_PROGRESS: 'lms-pill--success',
  SUBMITTED: 'lms-pill--accent',
  EVALUATING: 'lms-pill--accent',
  PASSED: 'lms-pill--success',
  FAILED: 'lms-pill--danger',
  COMPLETED: 'lms-pill--success',
  PROVISION_FAILED: 'lms-pill--danger',
  SUSPENDED: 'lms-pill--muted',
  EVAL_FAILED: 'lms-pill--danger',
  EXPIRED: 'lms-pill--muted',
  ABANDONED: 'lms-pill--muted',
};

/**
 * Doc §8.5 workspace layout is Dev A's territory (terminal/editor chrome
 * per PLAN.md's split). This page is Dev B's slice of it: the attempt
 * lifecycle status, task checklist shape, and the Check/Submit controls
 * doc §8.5's ASCII mockup shows on the left rail -- without the
 * terminal/editor pane, since that's the Environment Orchestrator's WS
 * connection (not yet built, Dev A's Phase 1 M1.5-M1.6).
 */
const EVALUATED_STATUSES: AttemptStatus[] = ['PASSED', 'FAILED', 'COMPLETED', 'EVAL_FAILED'];

export default function AttemptPage({ params }: { params: Promise<{ id: string }> }) {
  const { id } = use(params);
  const queryClient = useQueryClient();

  const { data: attempt, isLoading } = useQuery({
    queryKey: ['attempt', id],
    queryFn: () => api.getAttempt(id),
    refetchInterval: (query) =>
      query.state.data && ['PROVISIONING', 'EVALUATING'].includes(query.state.data.status) ? 1500 : false,
  });

  const { data: activity } = useQuery({
    queryKey: ['activity-version', attempt?.activity_version_id],
    queryFn: () => api.getActivityVersion(attempt!.activity_version_id),
    enabled: !!attempt,
  });

  const isEvaluated = !!attempt && EVALUATED_STATUSES.includes(attempt.status);

  const { data: evaluation } = useQuery({
    queryKey: ['attempt-evaluation', id],
    queryFn: () => api.getEvaluation(id),
    enabled: isEvaluated,
  });

  const { data: tasks } = useQuery({
    queryKey: ['attempt-tasks', id],
    queryFn: () => api.getTasks(id),
    enabled: isEvaluated,
  });

  const startMutation = useMutation({
    mutationFn: () => api.startAttempt(id),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['attempt', id] }),
  });

  const submitMutation = useMutation({
    mutationFn: () => api.submitAttempt(id),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['attempt', id] }),
  });

  if (isLoading) return <div className="mx-auto max-w-4xl px-6 py-10 text-[var(--ink-muted)]">Loading attempt…</div>;
  if (!attempt) return <div className="mx-auto max-w-4xl px-6 py-10 text-[var(--danger)]">Attempt not found.</div>;

  return (
    <div className="mx-auto max-w-4xl px-6 py-10">
      <div className="flex items-center justify-between">
        <div>
          <p className="font-mono-label">Attempt {attempt.id}</p>
          <h1 className="font-display mt-1 text-xl font-extrabold">{attempt.mode.replace('_', ' ')}</h1>
        </div>
        <span className={`lms-pill ${STATUS_COLOR[attempt.status]}`}>{attempt.status}</span>
      </div>

      <div className="mt-8 grid grid-cols-2 gap-4 text-sm">
        <Field label="Environment" value={attempt.environment_id ?? '—'} />
        <Field label="Created" value={new Date(attempt.created_at).toLocaleTimeString()} />
        <Field label="Started" value={attempt.started_at ? new Date(attempt.started_at).toLocaleTimeString() : '—'} />
        <Field
          label="Submitted"
          value={attempt.submitted_at ? new Date(attempt.submitted_at).toLocaleTimeString() : '—'}
        />
      </div>

      {(attempt.status === 'PROVISIONING' || attempt.status === 'EVALUATING') && (
        <p className="mt-8 text-sm text-[var(--ink-soft)]">
          {attempt.status === 'PROVISIONING' ? 'Provisioning environment…' : 'Evaluating…'} (auto-refreshing)
        </p>
      )}

      <div className="mt-10 flex gap-3">
        {attempt.status === 'READY' && (
          <button
            onClick={() => startMutation.mutate()}
            disabled={startMutation.isPending}
            className="lms-action-btn lms-action-btn--primary"
          >
            {startMutation.isPending ? 'Starting…' : 'Start working'}
          </button>
        )}
        {attempt.status === 'IN_PROGRESS' && (
          <button
            onClick={() => submitMutation.mutate()}
            disabled={submitMutation.isPending}
            className="lms-action-btn lms-action-btn--primary"
          >
            {submitMutation.isPending ? 'Submitting…' : 'Submit attempt'}
          </button>
        )}
      </div>

      {attempt.status === 'IN_PROGRESS' &&
        attempt.environment_id &&
        !attempt.environment_id.startsWith('fake-env-') && (
          <div className="mt-10 grid grid-cols-2 gap-4">
            <div className="h-[480px]">
              <WorkspaceEditor attemptId={id} />
            </div>
            <div className="h-[480px]">
              <WorkspaceTerminal attemptId={id} />
            </div>
          </div>
        )}

      {attempt.status === 'IN_PROGRESS' && activity?.spec_jsonb.tasks && activity.spec_jsonb.tasks.length > 0 && (
        <div className="lms-card mt-10 p-5">
          <h2 className="font-mono-label">Tasks</h2>
          <div className="mt-3 space-y-4">
            {activity.spec_jsonb.tasks.map((task) => (
              <div key={task.key} className="lms-divider-dashed pb-4 pt-4 first:pt-0 last:border-0 last:pb-0">
                <p className="text-sm text-[var(--ink)]">{task.title}</p>
                <HintPanel attemptId={id} taskKey={task.key} />
              </div>
            ))}
          </div>
        </div>
      )}

      {isEvaluated && evaluation && (
        <div className="lms-card mt-10 p-5">
          <div className="flex items-baseline justify-between">
            <h2 className="font-mono-label">Result</h2>
            <span
              className={`font-display text-2xl font-extrabold ${evaluation.passed ? 'text-[var(--success)]' : 'text-[var(--danger)]'}`}
            >
              {(Number(evaluation.final_score) * 100).toFixed(0)}%
            </span>
          </div>
          <p className="mt-1 text-xs text-[var(--ink-soft)]">{evaluation.profile_version_id}</p>

          <div className="mt-4 space-y-2">
            {Object.entries(evaluation.breakdown_jsonb).map(([key, crit]) => (
              <div key={key} className="flex items-center gap-3 text-sm">
                <span className="w-40 shrink-0 text-[var(--ink-muted)]">{key.replace(/_/g, ' ')}</span>
                <div className="h-1.5 flex-1 overflow-hidden rounded-full bg-[var(--inset)]">
                  <div className="h-full bg-[var(--accent)]" style={{ width: `${crit.value * 100}%` }} />
                </div>
                <span className="w-12 shrink-0 text-right font-mono text-xs text-[var(--ink-soft)]">
                  {(crit.value * 100).toFixed(0)}%
                </span>
              </div>
            ))}
          </div>

          {tasks && tasks.length > 0 && (
            <div className="lms-divider-dashed mt-6 pt-4">
              <h3 className="font-mono-label">Tasks</h3>
              <ul className="mt-2 space-y-1">
                {tasks.map((t) => (
                  <li key={t.task_key} className="flex items-center gap-2 text-sm">
                    <span
                      className={
                        t.status === 'PASSED'
                          ? 'text-[var(--success)]'
                          : t.status === 'FAILED'
                            ? 'text-[var(--danger)]'
                            : 'text-[var(--ink-soft)]'
                      }
                    >
                      {t.status === 'PASSED' ? '✓' : t.status === 'FAILED' ? '✗' : '·'}
                    </span>
                    <span className="text-[var(--ink)]">{t.task_key}</span>
                    <span className="text-xs text-[var(--ink-soft)]">({t.status.toLowerCase()})</span>
                  </li>
                ))}
              </ul>
            </div>
          )}
        </div>
      )}

      {attempt.environment_id?.startsWith('fake-env-') && (
        <p className="mt-8 rounded-md border border-[var(--warning)] bg-[var(--warning-soft)] p-3 text-xs text-[var(--warning)]">
          This environment was provisioned by FakeOrchestratorClient (Phase 0 stub) — no real
          terminal/container exists yet. The Environment Orchestrator (Dev A, Go service) will
          replace this.
        </p>
      )}
    </div>
  );
}

/**
 * Doc §7.5 step 38: "mentor offers the next hint level explicitly,
 * stating the score cost... 'I can give you a stronger hint -- that'll
 * cost about 3% on this task's score. Want it?' Transparency converts an
 * adversarial interaction into an informed choice." Two-step UI mirrors
 * the two-step API (preview = no side effect, reveal = commits + costs).
 * Revealed hints accumulate in local state so escalating through the
 * ladder shows the full trail, not just the latest hint.
 */
function HintPanel({ attemptId, taskKey }: { attemptId: string; taskKey: string }) {
  const [revealed, setRevealed] = useState<HintReveal[]>([]);

  const { data: preview, isLoading } = useQuery({
    queryKey: ['hint-preview', attemptId, taskKey, revealed.length],
    queryFn: () => api.previewHint(attemptId, taskKey),
  });

  const revealMutation = useMutation({
    mutationFn: () => api.revealHint(attemptId, taskKey),
    onSuccess: (hint) => setRevealed((prev) => [...prev, hint]),
  });

  return (
    <div className="mt-2">
      {revealed.map((hint) => (
        <p key={hint.nextLevel} className="lms-inset-field mt-1 p-2 text-xs text-[var(--ink-muted)]">
          <span className="text-[var(--ink-soft)]">Hint L{hint.nextLevel}:</span> {hint.text}
        </p>
      ))}

      {!isLoading && preview && (
        <button
          onClick={() => revealMutation.mutate()}
          disabled={revealMutation.isPending}
          className="lms-action-btn lms-action-btn--primary lms-action-btn--sm mt-2"
        >
          {revealMutation.isPending
            ? 'Getting hint…'
            : `Get a hint (−${(preview.penalty * 100).toFixed(0)}%)`}
        </button>
      )}

      {!isLoading && !preview && revealed.length > 0 && (
        <p className="mt-2 text-xs text-[var(--ink-soft)]">No more hints for this task.</p>
      )}
    </div>
  );
}

function Field({ label, value }: { label: string; value: string }) {
  return (
    <div className="lms-inset-field p-3">
      <p className="font-mono-label">{label}</p>
      <p className="mt-1 font-mono text-xs text-[var(--ink)]">{value}</p>
    </div>
  );
}
