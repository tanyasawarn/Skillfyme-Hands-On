'use client';

import { use, useState } from 'react';
import { useMutation, useQuery } from '@tanstack/react-query';
import { useRouter } from 'next/navigation';
import type {
  Attempt,
  AttemptCheckResult,
  AttemptEvaluation,
  AttemptStatus,
} from '@/lib/api-client';
import { api } from '@/lib/api-client';
import { useSession } from '@/lib/session';
import { toUserFacingError } from '@/lib/error-message';
import { makeIdempotencyKey } from '@/lib/idempotency';
import { catalogEntryRoute } from '@/lib/routes';
import { WorkspaceTerminal } from '@/components/WorkspaceTerminal';
import { WorkspaceEditor } from '@/components/WorkspaceEditor';
import { Button } from '@/components/ui/Button';
import { Badge } from '@/components/ui/Badge';
import { ATTEMPT_STATUS_META } from '@/lib/attempt-status';
import { Loader } from '@/components/ui/Loader';
import { PageContainer } from '@/components/ui/PageContainer';
import { EmptyState } from '@/components/ui/EmptyState';
import { ProgressBar } from '@/components/ui/ProgressBar';
import { SectionLabel } from '@/components/ui/SectionLabel';
import { formatPercent, formatMode } from '@/lib/format';
import { useActivityVersion } from '@/lib/use-activity-version';
import { useAttemptAction } from '@/lib/use-attempt-action';
import { HintPanel } from '@/components/attempt/HintPanel';
import { isProductionSim } from '@/lib/sim';
import { SimShell } from '@/components/sim/SimShell';
import { SimDebrief } from '@/components/sim/SimDebrief';

/**
 * Doc §8.5 workspace layout is Dev A's territory (terminal/editor chrome
 * per PLAN.md's split). This page is Dev B's slice of it: the attempt
 * lifecycle status, task checklist shape, and the Check/Submit controls
 * doc §8.5's ASCII mockup shows on the left rail -- without the
 * terminal/editor pane, since that's the Environment Orchestrator's WS
 * connection (not yet built, Dev A's Phase 1 M1.5-M1.6).
 *
 * Layout: a fixed-viewport workspace (task rail + editor/terminal),
 * not a long scrolling document -- while IN_PROGRESS this behaves like
 * an IDE, not an article. Once evaluated, the workspace panes are gone
 * (the environment is torn down server-side anyway) and the page reverts
 * to a normal scrolling results view.
 */
const EVALUATED_STATUSES: AttemptStatus[] = ['PASSED', 'FAILED', 'COMPLETED', 'EVAL_FAILED'];

export default function AttemptPage({ params }: { params: Promise<{ id: string }> }) {
  const { id } = use(params);

  const { data: attempt, isLoading } = useQuery({
    queryKey: ['attempt', id],
    queryFn: () => api.getAttempt(id),
    refetchInterval: (query) =>
      query.state.data && ['PROVISIONING', 'EVALUATING'].includes(query.state.data.status) ? 1500 : false,
  });

  const { data: activity } = useActivityVersion(attempt?.activity_version_id);

  const isEvaluated = !!attempt && EVALUATED_STATUSES.includes(attempt.status);
  const isWorkspaceActive =
    attempt?.status === 'IN_PROGRESS' &&
    !!attempt.environment_id &&
    !attempt.environment_id.startsWith('fake-env-');

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

  const startMutation = useAttemptAction(id, api.startAttempt);
  const submitMutation = useAttemptAction(id, api.submitAttempt);

  // "Check my work" (doc §6.3 learner-triggered validation): runs the
  // validators without ending the attempt so the learner sees which
  // tasks aren't done yet *while they can still fix them*, rather than
  // finding out from the score screen with no way back.
  const [checkResult, setCheckResult] = useState<AttemptCheckResult | null>(null);
  const [checkError, setCheckError] = useState<string | null>(null);
  const checkMutation = useMutation({
    mutationFn: () => api.checkAttempt(id),
    onSuccess: (result) => {
      setCheckError(null);
      setCheckResult(result);
    },
    onError: (err) => setCheckError(toUserFacingError(err).headline),
  });

  // Guard the blind submit: if a check has run and required tasks are
  // still failing (or no check has been run at all), confirm before
  // ending the attempt.
  const handleSubmit = () => {
    const unpassed = checkResult
      ? checkResult.tasks.filter((t) => t.required && !t.passed).map((t) => t.task_key)
      : null;
    const warning = !checkResult
      ? "You haven't run \"Check my work\" yet. Submit and score this attempt now?"
      : unpassed && unpassed.length > 0
        ? `These required tasks aren't passing yet: ${unpassed.join(', ')}. Submit anyway?`
        : null;
    if (warning && !window.confirm(warning)) return;
    submitMutation.mutate();
  };

  if (isLoading) {
    return (
      <PageContainer maxWidth="4xl" spacing="py-10">
        <Loader label="Loading attempt…" />
      </PageContainer>
    );
  }
  if (!attempt) {
    return (
      <PageContainer maxWidth="4xl" spacing="py-10">
        <p className="text-[var(--danger)]">Attempt not found.</p>
      </PageContainer>
    );
  }

  const taskSpecs = activity?.spec_jsonb.tasks ?? [];
  // Defaults to showing the editor while the spec hasn't loaded yet
  // (activity query starts after attempt) rather than flashing a
  // terminal-only layout that then jumps to two panes once it resolves.
  const needsEditor = activity ? (activity.spec_jsonb.surfaces ?? ['terminal', 'editor']).includes('editor') : true;

  // PRODUCTION_SIM attempts get the incident-response rail (ticket, SLA
  // timer, symptoms, escalation, incident-note editor) instead of the
  // plain task list, and the debrief screen instead of ResultPanel.
  const isSim = isProductionSim(attempt.mode);

  return (
    <div className={isWorkspaceActive ? 'flex h-[calc(100vh-56px)] flex-col overflow-hidden' : ''}>
      <AttemptHeader
        attempt={attempt}
        compact={isWorkspaceActive}
        onStart={() => startMutation.mutate()}
        onCheck={() => checkMutation.mutate()}
        onSubmit={handleSubmit}
        starting={startMutation.isPending}
        checking={checkMutation.isPending}
        submitting={submitMutation.isPending}
      />

      {isWorkspaceActive && (checkResult || checkError) && (
        <CheckResultBar
          result={checkResult}
          error={checkError}
          onDismiss={() => {
            setCheckResult(null);
            setCheckError(null);
          }}
        />
      )}

      {isWorkspaceActive ? (
        <div className="grid min-h-0 flex-1 grid-cols-[360px_1fr] gap-4 px-6 pb-6">
          {isSim && activity ? (
            <SimShell attemptId={id} attempt={attempt} spec={activity.spec_jsonb} />
          ) : (
            <TaskRail attemptId={id} tasks={taskSpecs} />
          )}
          {needsEditor ? (
            <div className="grid min-h-0 grid-cols-2 gap-4">
              <div className="min-h-0">
                <WorkspaceEditor attemptId={id} />
              </div>
              <div className="min-h-0">
                <WorkspaceTerminal attemptId={id} />
              </div>
            </div>
          ) : (
            <div className="min-h-0">
              <WorkspaceTerminal attemptId={id} />
            </div>
          )}
        </div>
      ) : (
        <PageContainer spacing="pb-16">
          {(attempt.status === 'PROVISIONING' || attempt.status === 'EVALUATING') && (
            <div className="lms-card mt-6 flex items-center gap-3 p-5">
              <span className="workspace-spinner" aria-hidden />
              <p className="text-sm text-[var(--ink-muted)]">
                {attempt.status === 'PROVISIONING'
                  ? 'Setting up your environment…'
                  : 'Scoring your submission…'}
              </p>
            </div>
          )}

          {attempt.status === 'IN_PROGRESS' && !isWorkspaceActive && (
            <div className="lms-card mt-6 p-5 text-sm text-[var(--warning)]">
              This attempt has no live workspace (a stub environment from local testing). Submit
              when ready.
            </div>
          )}

          {isEvaluated && evaluation && (
            isSim && activity ? (
              <SimDebrief evaluation={evaluation} tasks={tasks} spec={activity.spec_jsonb} />
            ) : (
              <ResultPanel evaluation={evaluation} tasks={tasks} />
            )
          )}

          {/*
           * Post-evaluation, the workspace panes are gone and the
           * attempt is terminal (PASSED/FAILED/COMPLETED/EVAL_FAILED) --
           * without an explicit next action the learner is stranded on
           * the score card with no way forward (bug: "quiz flow break
           * after submission"). Always render a recovery footer once
           * evaluated, even when the evaluation payload itself failed to
           * load, so there is never a dead end.
           */}
          {isEvaluated && (
            <ResultActions
              activityVersionId={attempt.activity_version_id}
            />
          )}

          {attempt.environment_id?.startsWith('fake-env-') && (
            <p className="lms-inset-field mt-6 p-3 text-xs text-[var(--warning)]">
              This environment was provisioned by FakeOrchestratorClient (Phase 0 stub) — no real
              terminal/container exists yet.
            </p>
          )}
        </PageContainer>
      )}
    </div>
  );
}

function AttemptHeader({
  attempt,
  compact,
  onStart,
  onCheck,
  onSubmit,
  starting,
  checking,
  submitting,
}: {
  attempt: Attempt;
  compact: boolean;
  onStart: () => void;
  onCheck: () => void;
  onSubmit: () => void;
  starting: boolean;
  checking: boolean;
  submitting: boolean;
}) {
  return (
    <div
      className={`flex shrink-0 items-center justify-between gap-4 border-b border-[var(--border)] ${
        compact ? 'px-6 py-3' : 'mx-auto max-w-3xl px-6 pt-8 pb-4'
      }`}
    >
      <div className="min-w-0">
        <div className="flex items-center gap-2">
          <h1 className="font-display truncate text-lg font-extrabold">{formatMode(attempt.mode)}</h1>
          <Badge variant={ATTEMPT_STATUS_META[attempt.status].variant}>
            {ATTEMPT_STATUS_META[attempt.status].label}
          </Badge>
        </div>
        {!compact && (
          <p className="mt-1 font-mono text-[11px] text-[var(--ink-soft)]">
            {attempt.id} · started {attempt.started_at ? new Date(attempt.started_at).toLocaleTimeString() : '—'}
          </p>
        )}
      </div>

      <div className="flex shrink-0 items-center gap-3">
        {compact && (
          <p className="font-mono text-[11px] text-[var(--ink-soft)]">
            env {attempt.environment_id?.slice(0, 8) ?? '—'}
          </p>
        )}
        {attempt.status === 'READY' && (
          <Button onClick={onStart} disabled={starting}>
            {starting ? 'Starting…' : 'Start working'}
          </Button>
        )}
        {attempt.status === 'IN_PROGRESS' && (
          <>
            <Button
              variant="outline"
              onClick={onCheck}
              disabled={checking || submitting}
            >
              {checking ? 'Checking…' : 'Check my work'}
            </Button>
            <Button onClick={onSubmit} disabled={submitting || checking}>
              {submitting ? 'Submitting…' : 'Submit attempt'}
            </Button>
          </>
        )}
      </div>
    </div>
  );
}

/**
 * Result strip for a "Check my work" run, shown under the workspace
 * header while IN_PROGRESS. This is the feedback the raw PTY terminal
 * can't give: the terminal just echoes whatever the shell prints (an
 * unclosed quote, a typo'd command), with no notion of whether a task
 * is actually done -- this bar is where "task 2 isn't passing yet"
 * surfaces before the learner submits.
 */
function CheckResultBar({
  result,
  error,
  onDismiss,
}: {
  result: AttemptCheckResult | null;
  error: string | null;
  onDismiss: () => void;
}) {
  if (error) {
    return (
      <div className="flex shrink-0 items-center justify-between gap-3 border-b border-[var(--danger)] bg-[var(--danger-soft)] px-6 py-2 text-xs text-[var(--danger)]">
        <span>Couldn&apos;t check your work: {error}</span>
        <button onClick={onDismiss} className="shrink-0 underline opacity-80">
          dismiss
        </button>
      </div>
    );
  }
  if (!result) return null;

  const allPassed = result.all_required_passed;
  return (
    <div
      className={`flex shrink-0 flex-wrap items-center gap-x-4 gap-y-1 border-b px-6 py-2 text-xs ${
        allPassed
          ? 'border-[var(--success)] bg-[var(--success-soft)] text-[var(--success)]'
          : 'border-[var(--warning)] bg-[var(--warning-soft)] text-[var(--warning)]'
      }`}
    >
      <span className="font-semibold">
        {allPassed
          ? 'All required tasks pass — ready to submit.'
          : 'Not all required tasks pass yet:'}
      </span>
      <span className="flex flex-wrap items-center gap-x-3 gap-y-1">
        {result.tasks.map((t) => (
          <span key={t.task_key} className="inline-flex items-center gap-1">
            <span aria-hidden>{t.passed ? '✓' : '✕'}</span>
            <span className="font-mono">{t.task_key}</span>
            {!t.required && <span className="opacity-70">(optional)</span>}
          </span>
        ))}
      </span>
      <button onClick={onDismiss} className="ml-auto shrink-0 underline opacity-80">
        dismiss
      </button>
    </div>
  );
}

/**
 * The left rail: what the learner is actually here to read while
 * working. Kept visible and scrollable independent of the editor/
 * terminal pane so instructions never disappear mid-task, unlike the
 * old layout where tasks/hints sat below the workspace and required
 * scrolling away from the terminal to re-read them.
 */
function TaskRail({
  attemptId,
  tasks,
}: {
  attemptId: string;
  tasks: Array<{ key: string; title: string; required: boolean; instructions_md: string }>;
}) {
  if (tasks.length === 0) {
    return <EmptyState variant="card">No tasks for this activity.</EmptyState>;
  }

  return (
    <div className="lms-card flex min-h-0 flex-col overflow-hidden">
      <div className="shrink-0 border-b border-[var(--border)] px-4 py-3">
        <SectionLabel>Tasks</SectionLabel>
      </div>
      <div className="min-h-0 flex-1 overflow-y-auto px-4 py-3">
        {tasks.map((task, i) => (
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
        ))}
      </div>
    </div>
  );
}


function ResultPanel({
  evaluation,
  tasks,
}: {
  evaluation: AttemptEvaluation;
  tasks: Array<{ task_key: string; status: string }> | undefined;
}) {
  return (
    <div className="lms-card mt-6 overflow-hidden">
      <div
        className={`flex items-center justify-between px-6 py-5 ${
          evaluation.passed ? 'bg-[var(--success-soft)]' : 'bg-[var(--danger-soft)]'
        }`}
      >
        <div>
          <SectionLabel as="p">{evaluation.passed ? 'Passed' : 'Not passed'}</SectionLabel>
          <p className="mt-0.5 text-xs text-[var(--ink-soft)]">{evaluation.profile_version_id}</p>
        </div>
        <p
          className={`font-display text-4xl font-extrabold tabular-nums ${
            evaluation.passed ? 'text-[var(--success)]' : 'text-[var(--danger)]'
          }`}
        >
          {formatPercent(Number(evaluation.final_score))}
        </p>
      </div>

      <div className="p-6">
        <div className="space-y-3">
          {Object.entries(evaluation.breakdown_jsonb).map(([key, crit]) => (
            <div key={key} className="flex items-center gap-3 text-sm">
              <span className="w-40 shrink-0 capitalize text-[var(--ink-muted)]">{key.replace(/_/g, ' ')}</span>
              <ProgressBar value={crit.value} fillClassName="bg-[var(--accent)]" animated />
              <span className="w-11 shrink-0 text-right font-mono text-xs tabular-nums text-[var(--ink-soft)]">
                {formatPercent(crit.value)}
              </span>
            </div>
          ))}
        </div>

        {tasks && tasks.length > 0 && (
          <div className="lms-divider-dashed mt-6 pt-5">
            <SectionLabel as="h3">Tasks</SectionLabel>
            <ul className="mt-3 space-y-2">
              {tasks.map((t) => (
                <li key={t.task_key} className="flex items-center gap-2.5 text-sm">
                  <TaskStatusBadge status={t.status} />
                  <span className="text-[var(--ink)]">{t.task_key}</span>
                  <span className="text-xs capitalize text-[var(--ink-soft)]">{t.status.toLowerCase()}</span>
                </li>
              ))}
            </ul>
          </div>
        )}
      </div>
    </div>
  );
}

/**
 * Recovery footer shown on every evaluated (terminal) attempt. An
 * attempt never transitions back out of PASSED/FAILED/COMPLETED/
 * EVAL_FAILED, so "retry" means starting a *fresh* attempt on the same
 * activity version -- the same create -> provision chain the catalog
 * detail page runs. "Back to catalog" is the always-safe exit and never
 * depends on a network call succeeding, so the learner can always leave
 * even if a new attempt can't be created (e.g. retry cooldown).
 */
function ResultActions({ activityVersionId }: { activityVersionId: string }) {
  const router = useRouter();
  const [retryError, setRetryError] = useState<string | null>(null);
  const session = useSession();

  const retryMutation = useMutation({
    mutationFn: async () => {
      if (!session) throw new Error('session not ready');
      const idempotencyKey = makeIdempotencyKey(`retry-${activityVersionId}`);
      const created = await api.createAttempt(
        {
          tenant_id: session.tenantId,
          user_id: session.userId,
          activity_version_id: activityVersionId,
        },
        idempotencyKey,
      );
      await api.provisionAttempt(created.id);
      return created.id;
    },
    onSuccess: (attemptId) => router.push(`/attempts/${attemptId}`),
    onError: (err) => setRetryError(toUserFacingError(err).headline),
  });

  return (
    <div className="lms-card mt-6 flex flex-wrap items-center gap-3 p-5">
      <Button
        onClick={() => {
          setRetryError(null);
          retryMutation.mutate();
        }}
        disabled={retryMutation.isPending || !session}
      >
        {retryMutation.isPending ? 'Starting…' : 'Try again'}
      </Button>
      <Button
        variant="outline"
        onClick={() => router.push(catalogEntryRoute(activityVersionId))}
      >
        Back to activity
      </Button>
      <Button variant="outline" onClick={() => router.push('/history')}>
        View history
      </Button>
      {retryError && (
        <p className="w-full text-sm text-[var(--danger)]">Could not start a new attempt: {retryError}</p>
      )}
    </div>
  );
}

function TaskStatusBadge({ status }: { status: string }) {
  if (status === 'PASSED') return <Badge shape="circle" variant="success">✓</Badge>;
  if (status === 'FAILED') return <Badge shape="circle" variant="danger">✕</Badge>;
  return <Badge shape="circle" variant="muted">·</Badge>;
}
