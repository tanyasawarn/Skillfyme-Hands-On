'use client';

import { use, useState } from 'react';
import { useMutation } from '@tanstack/react-query';
import { useRouter } from 'next/navigation';
import { api } from '@/lib/api-client';
import { useActivityVersion } from '@/lib/use-activity-version';
import { useSession } from '@/lib/session';
import { toUserFacingError } from '@/lib/error-message';
import { formatCurrency, formatMode } from '@/lib/format';
import { Button } from '@/components/ui/Button';
import { Loader } from '@/components/ui/Loader';
import { Alert } from '@/components/ui/Alert';
import { PageContainer } from '@/components/ui/PageContainer';
import { SectionLabel } from '@/components/ui/SectionLabel';
import { attemptRoute } from '@/lib/routes';
import { makeIdempotencyKey } from '@/lib/idempotency';

export default function ActivityDetailPage({
  params,
}: {
  params: Promise<{ activityVersionId: string }>;
}) {
  const { activityVersionId } = use(params);
  const router = useRouter();
  const [startError, setStartError] = useState<string | null>(null);

  const { data, isLoading, isError } = useActivityVersion(activityVersionId);
  const session = useSession();

  const startMutation = useMutation({
    mutationFn: async () => {
      if (!session) throw new Error('session not ready');
      const idempotencyKey = makeIdempotencyKey(`start-${activityVersionId}`);
      const created = await api.createAttempt(
        { tenant_id: session.tenantId, user_id: session.userId, activity_version_id: activityVersionId },
        idempotencyKey,
      );
      // Doc §4.1 steps 16-22: create then provision are separate calls
      // (Operation-not-blocked-request principle, §8.3) -- the UI chains
      // them here since Phase 1 has no client-side polling/streaming yet.
      await api.provisionAttempt(created.id);
      return created.id;
    },
    onSuccess: (attemptId) => router.push(attemptRoute(attemptId)),
    onError: (err) => setStartError(toUserFacingError(err).headline),
  });

  if (isLoading) {
    return (
      <PageContainer spacing="py-10">
        <Loader label="Loading…" />
      </PageContainer>
    );
  }
  if (isError || !data) {
    return (
      <PageContainer spacing="py-10">
        <Alert>Could not load this activity. It may not be published, or practice-core isn&apos;t running.</Alert>
      </PageContainer>
    );
  }

  const spec = data.spec_jsonb ?? {};

  return (
    <PageContainer spacing="py-10">
      <SectionLabel as="p">{formatMode(data.mode)}</SectionLabel>
      <h1 className="font-display mt-1 text-2xl font-extrabold">{spec.meta?.title ?? data.slug}</h1>
      {spec.meta?.summary && <p className="mt-3 text-[var(--ink-muted)]">{spec.meta.summary}</p>}

      <div className="mt-4 flex gap-3 text-xs text-[var(--ink-soft)]">
        <span>{data.difficulty_level}</span>
        <span>·</span>
        <span>{data.estimated_minutes ? `~${data.estimated_minutes} min` : 'duration unknown'}</span>
        <span>·</span>
        <span>{data.cost_budget_usd ? formatCurrency(Number(data.cost_budget_usd)) : '$?'} est. cost</span>
      </div>

      {spec.objectives && spec.objectives.length > 0 && (
        <div className="mt-8">
          <SectionLabel>Objectives</SectionLabel>
          <ul className="mt-2 list-disc space-y-1 pl-5 text-sm text-[var(--ink-muted)]">
            {spec.objectives.map((obj, i) => (
              <li key={i}>{obj}</li>
            ))}
          </ul>
        </div>
      )}

      {spec.tasks && spec.tasks.length > 0 && (
        <div className="mt-8">
          <SectionLabel>Tasks</SectionLabel>
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

      <Button
        onClick={() => {
          setStartError(null);
          startMutation.mutate();
        }}
        disabled={startMutation.isPending || !session}
        className="mt-10"
      >
        {startMutation.isPending ? 'Starting…' : 'Start attempt'}
      </Button>

      {startError && (
        <p className="mt-3 text-sm text-[var(--danger)]">
          Could not start: {startError}
        </p>
      )}
    </PageContainer>
  );
}
