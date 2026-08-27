'use client';

import Link from 'next/link';
import { useQueries, useQuery } from '@tanstack/react-query';
import { api, type Attempt } from '@/lib/api-client';
import { useSession } from '@/lib/session';
import { Badge } from '@/components/ui/Badge';
import { ATTEMPT_STATUS_META } from '@/lib/attempt-status';
import { PageContainer } from '@/components/ui/PageContainer';
import { EmptyState } from '@/components/ui/EmptyState';
import { CardLink } from '@/components/ui/CardLink';
import { QueryBoundary } from '@/components/ui/QueryBoundary';
import { formatPercent, formatMode } from '@/lib/format';
import { activityVersionQueryOptions } from '@/lib/use-activity-version';
import { attemptRoute } from '@/lib/routes';
import { SectionLabel } from '@/components/ui/SectionLabel';

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
  const session = useSession();
  const { data: attempts, isLoading, isError, error } = useQuery({
    queryKey: ['attempts', session?.userId],
    queryFn: () => api.listAttempts(session!.userId),
    enabled: !!session,
  });

  const sorted = attempts
    ? [...attempts].sort((a, b) => new Date(b.created_at).getTime() - new Date(a.created_at).getTime())
    : [];

  const activityVersionIds = [...new Set(sorted.map((a) => a.activity_version_id))];
  const activityQueries = useQueries({
    queries: activityVersionIds.map((id) => activityVersionQueryOptions(id)),
  });
  const titleByVersionId = new Map<string, string>();
  activityVersionIds.forEach((id, i) => {
    const title = activityQueries[i].data?.spec_jsonb.meta?.title;
    if (title) titleByVersionId.set(id, title);
  });

  return (
    <PageContainer maxWidth="4xl" spacing="py-10">
      <div>
        <h1 className="font-display text-2xl font-extrabold">History</h1>
        <p className="mt-1 max-w-xl text-sm text-[var(--ink-muted)]">
          Every practice attempt you&apos;ve started, most recent first.
        </p>
      </div>

      <QueryBoundary isLoading={isLoading || !session} isError={isError} error={error} loadingLabel="Loading history…">
        {attempts && attempts.length === 0 && (
          <EmptyState>
            No attempts yet. Head to the <Link href="/catalog" className="underline">catalog</Link> to start your first lab.
          </EmptyState>
        )}

        {sorted.length > 0 && (
          <div className="mt-8 space-y-3">
            {sorted.map((attempt) => (
              <HistoryRow key={attempt.id} attempt={attempt} title={titleByVersionId.get(attempt.activity_version_id)} />
            ))}
          </div>
        )}
      </QueryBoundary>
    </PageContainer>
  );
}

function HistoryRow({ attempt, title }: { attempt: Attempt; title: string | undefined }) {
  const meta = ATTEMPT_STATUS_META[attempt.status] ?? { label: attempt.status, variant: 'muted' as const };
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
    <CardLink variant="row" href={attemptRoute(attempt.id)}>
      <div className="min-w-0 flex-1">
        <p className="truncate font-medium text-[var(--ink)]">{title ?? attempt.activity_id}</p>
        <p className="mt-1 text-xs text-[var(--ink-muted)]">
          {formatMode(attempt.mode)} · {new Date(attempt.created_at).toLocaleString()}
        </p>
      </div>
      <div className="flex shrink-0 items-center gap-3">
        {evaluation && (
          <SectionLabel as="span" className="text-[var(--ink-muted)]">
            {formatPercent(Number(evaluation.final_score))}
          </SectionLabel>
        )}
        <Badge variant={meta.variant}>{meta.label}</Badge>
      </div>
    </CardLink>
  );
}
