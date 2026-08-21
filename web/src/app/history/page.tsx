'use client';

import Link from 'next/link';
import { useQueries, useQuery } from '@tanstack/react-query';
import { api, type Attempt, type AttemptStatus } from '@/lib/api-client';
import { DEMO_USER_ID } from '@/lib/demo-context';

const STATUS_PILL: Record<AttemptStatus, { label: string; variant: string }> = {
  CREATED: { label: 'Created', variant: 'lms-pill--muted' },
  PROVISIONING: { label: 'Provisioning', variant: 'lms-pill--accent' },
  READY: { label: 'Ready', variant: 'lms-pill--accent' },
  IN_PROGRESS: { label: 'In progress', variant: 'lms-pill--accent' },
  SUBMITTED: { label: 'Submitted', variant: 'lms-pill--accent' },
  EVALUATING: { label: 'Evaluating', variant: 'lms-pill--accent' },
  PASSED: { label: 'Passed', variant: 'lms-pill--success' },
  COMPLETED: { label: 'Completed', variant: 'lms-pill--success' },
  FAILED: { label: 'Failed', variant: 'lms-pill--danger' },
  EVAL_FAILED: { label: 'Evaluation failed', variant: 'lms-pill--danger' },
  PROVISION_FAILED: { label: 'Provisioning failed', variant: 'lms-pill--danger' },
  EXPIRED: { label: 'Expired', variant: 'lms-pill--warning' },
  ABANDONED: { label: 'Abandoned', variant: 'lms-pill--warning' },
  SUSPENDED: { label: 'Suspended', variant: 'lms-pill--warning' },
};

/**
 * Doc §1.2-adjacent: a learner's past attempts, newest first. Attempt
 * rows carry only activity_version_id (attempt.controller.ts's list
 * response has no title/score join), so each row's title comes from a
 * second query per distinct activity_version_id -- batched via
 * useQueries rather than N sequential awaits, and React Query's cache
 * naturally dedupes repeated attempts at the same activity (a retried
 * lab shows up once in the query cache, not once per row fetched).
 */
export default function HistoryPage() {
  const { data: attempts, isLoading, isError, error } = useQuery({
    queryKey: ['attempts', DEMO_USER_ID],
    queryFn: () => api.listAttempts(DEMO_USER_ID),
  });

  const sorted = attempts
    ? [...attempts].sort((a, b) => new Date(b.created_at).getTime() - new Date(a.created_at).getTime())
    : [];

  const activityVersionIds = [...new Set(sorted.map((a) => a.activity_version_id))];
  const activityQueries = useQueries({
    queries: activityVersionIds.map((id) => ({
      queryKey: ['activity-version', id],
      queryFn: () => api.getActivityVersion(id),
    })),
  });
  const titleByVersionId = new Map<string, string>();
  activityVersionIds.forEach((id, i) => {
    const title = activityQueries[i].data?.spec_jsonb.meta?.title;
    if (title) titleByVersionId.set(id, title);
  });

  return (
    <div className="mx-auto max-w-4xl px-6 py-10">
      <div>
        <h1 className="font-display text-2xl font-extrabold">History</h1>
        <p className="mt-1 max-w-xl text-sm text-[var(--ink-muted)]">
          Every practice attempt you&apos;ve started, most recent first.
        </p>
      </div>

      {isLoading && <p className="mt-8 text-[var(--ink-muted)]">Loading history…</p>}

      {isError && (
        <div className="mt-8 rounded-md border border-[var(--danger)] bg-[var(--danger-soft)] p-4 text-sm text-[var(--danger)]">
          Could not reach practice-core at {process.env.NEXT_PUBLIC_API_BASE_URL}. Is it running
          (`npm run start:dev` in /practice-core)?
          <pre className="mt-2 whitespace-pre-wrap text-xs opacity-70">{String(error)}</pre>
        </div>
      )}

      {attempts && attempts.length === 0 && (
        <p className="mt-8 text-[var(--ink-soft)]">
          No attempts yet. Head to the <Link href="/catalog" className="underline">catalog</Link> to start your first lab.
        </p>
      )}

      {sorted.length > 0 && (
        <div className="mt-8 space-y-3">
          {sorted.map((attempt) => (
            <HistoryRow key={attempt.id} attempt={attempt} title={titleByVersionId.get(attempt.activity_version_id)} />
          ))}
        </div>
      )}
    </div>
  );
}

function HistoryRow({ attempt, title }: { attempt: Attempt; title: string | undefined }) {
  const pill = STATUS_PILL[attempt.status] ?? { label: attempt.status, variant: 'lms-pill--muted' };
  const { data: evaluation } = useQuery({
    queryKey: ['evaluation', attempt.id],
    queryFn: () => api.getEvaluation(attempt.id),
    // Evaluation only exists once an attempt has actually been scored --
    // skip the request entirely for attempts that never got that far
    // (still in progress, abandoned before submit), rather than firing a
    // request that just returns null every time.
    enabled: ['PASSED', 'FAILED', 'COMPLETED', 'EVAL_FAILED'].includes(attempt.status),
  });

  return (
    <Link
      href={`/attempts/${attempt.id}`}
      className="flex items-center justify-between gap-4 rounded-lg border border-[var(--border)] bg-[var(--surface)] p-4 transition hover:border-[var(--accent)]"
    >
      <div className="min-w-0 flex-1">
        <p className="truncate font-medium text-[var(--ink)]">{title ?? attempt.activity_id}</p>
        <p className="mt-1 text-xs text-[var(--ink-muted)]">
          {attempt.mode.replace('_', ' ')} · {new Date(attempt.created_at).toLocaleString()}
        </p>
      </div>
      <div className="flex shrink-0 items-center gap-3">
        {evaluation && (
          <span className="font-mono-label text-[var(--ink-muted)]">
            {Math.round(Number(evaluation.final_score) * 100)}%
          </span>
        )}
        <span className={`lms-pill ${pill.variant}`}>{pill.label}</span>
      </div>
    </Link>
  );
}
