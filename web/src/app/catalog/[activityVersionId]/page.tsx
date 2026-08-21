'use client';

import { use, useState } from 'react';
import { useMutation, useQuery } from '@tanstack/react-query';
import { useRouter } from 'next/navigation';
import { api } from '@/lib/api-client';
import { DEMO_TENANT_ID, DEMO_USER_ID } from '@/lib/demo-context';

export default function ActivityDetailPage({
  params,
}: {
  params: Promise<{ activityVersionId: string }>;
}) {
  const { activityVersionId } = use(params);
  const router = useRouter();
  const [startError, setStartError] = useState<string | null>(null);

  const { data, isLoading, isError } = useQuery({
    queryKey: ['activity-version', activityVersionId],
    queryFn: () => api.getActivityVersion(activityVersionId),
  });

  const startMutation = useMutation({
    mutationFn: async () => {
      const idempotencyKey = `web-start-${activityVersionId}-${Date.now()}`;
      const created = await api.createAttempt(
        { tenant_id: DEMO_TENANT_ID, user_id: DEMO_USER_ID, activity_version_id: activityVersionId },
        idempotencyKey,
      );
      // Doc §4.1 steps 16-22: create then provision are separate calls
      // (Operation-not-blocked-request principle, §8.3) -- the UI chains
      // them here since Phase 1 has no client-side polling/streaming yet.
      await api.provisionAttempt(created.id);
      return created.id;
    },
    onSuccess: (attemptId) => router.push(`/attempts/${attemptId}`),
    onError: (err) => setStartError(String(err)),
  });

  if (isLoading) return <div className="mx-auto max-w-3xl px-6 py-10 text-[var(--ink-muted)]">Loading…</div>;
  if (isError || !data) {
    return (
      <div className="mx-auto max-w-3xl px-6 py-10 text-[var(--danger)]">
        Could not load this activity. It may not be published, or practice-core isn&apos;t running.
      </div>
    );
  }

  const spec = data.spec_jsonb ?? {};

  return (
    <div className="mx-auto max-w-3xl px-6 py-10">
      <p className="font-mono-label">{data.mode.replace('_', ' ')}</p>
      <h1 className="font-display mt-1 text-2xl font-extrabold">{spec.meta?.title ?? data.slug}</h1>
      {spec.meta?.summary && <p className="mt-3 text-[var(--ink-muted)]">{spec.meta.summary}</p>}

      <div className="mt-4 flex gap-3 text-xs text-[var(--ink-soft)]">
        <span>{data.difficulty_level}</span>
        <span>·</span>
        <span>{data.estimated_minutes ? `~${data.estimated_minutes} min` : 'duration unknown'}</span>
        <span>·</span>
        <span>${data.cost_budget_usd ? Number(data.cost_budget_usd).toFixed(2) : '?'} est. cost</span>
      </div>

      {spec.objectives && spec.objectives.length > 0 && (
        <div className="mt-8">
          <h2 className="font-mono-label">Objectives</h2>
          <ul className="mt-2 list-disc space-y-1 pl-5 text-sm text-[var(--ink-muted)]">
            {spec.objectives.map((obj, i) => (
              <li key={i}>{obj}</li>
            ))}
          </ul>
        </div>
      )}

      {spec.tasks && spec.tasks.length > 0 && (
        <div className="mt-8">
          <h2 className="font-mono-label">Tasks</h2>
          <ol className="mt-2 space-y-1 text-sm text-[var(--ink-muted)]">
            {spec.tasks.map((task) => (
              <li key={task.key}>
                {task.title}
                {task.required && <span className="ml-2 text-xs text-[var(--ink-soft)]">(required)</span>}
              </li>
            ))}
          </ol>
        </div>
      )}

      <button
        onClick={() => {
          setStartError(null);
          startMutation.mutate();
        }}
        disabled={startMutation.isPending}
        className="lms-action-btn lms-action-btn--primary mt-10"
      >
        {startMutation.isPending ? 'Starting…' : 'Start attempt'}
      </button>

      {startError && (
        <p className="mt-3 text-sm text-[var(--danger)]">
          Could not start: {startError}
        </p>
      )}
    </div>
  );
}
