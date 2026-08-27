'use client';

import Link from 'next/link';
import { useQuery } from '@tanstack/react-query';
import { api } from '@/lib/api-client';
import { useSession } from '@/lib/session';
import { Loader } from '@/components/ui/Loader';
import { PageContainer } from '@/components/ui/PageContainer';
import { EmptyState } from '@/components/ui/EmptyState';
import { ProgressBar } from '@/components/ui/ProgressBar';
import { CardLink } from '@/components/ui/CardLink';
import { SectionLabel } from '@/components/ui/SectionLabel';
import { formatPercent, formatMode } from '@/lib/format';
import { masteryBandFillClassName } from '@/lib/mastery';
import { attemptRoute, catalogEntryRoute } from '@/lib/routes';

const REASON_LABEL: Record<string, (params: Record<string, unknown>) => string> = {
  CURRICULUM_ADJACENT: (p) => `Related to ${p.topic ?? 'your current topic'}`,
  REMEDIATION: (p) =>
    `Recommended because your mastery of this skill is ${p.mastery !== undefined ? formatPercent(Number(p.mastery)) : 'low'}`,
};

/**
 * Doc §1.2: "Home is a decision surface, not a dashboard. Its job is to
 * make the next click obvious. Maximum three primary CTAs: Continue
 * attempt, Recommended next, Fix a weak skill." This is that page, now
 * that RecommendationService (M1.11) and mastery listing (rest of M1.13)
 * both have real data behind them.
 */
export default function Home() {
  const session = useSession();
  const { data, isLoading } = useQuery({
    queryKey: ['dashboard', session?.userId],
    queryFn: () => api.getDashboard(session!.userId, session!.tenantId),
    enabled: !!session,
  });

  return (
    <PageContainer spacing="py-12">
      <h1 className="font-display text-2xl font-extrabold">Practice Engine</h1>
      <p className="mt-1 text-sm text-[var(--ink-muted)]">
        Learn → Follow → Implement → Troubleshoot → Design → Build → Defend
      </p>

      {(isLoading || !session) && <Loader className="mt-10" label="Loading…" />}

      {data && (
        <div className="mt-10 space-y-8">
          {data.continueAttempt && (
            <section>
              <SectionLabel>Continue</SectionLabel>
              <CardLink
                variant="plain"
                href={attemptRoute(data.continueAttempt.id)}
                className="mt-2 border-l-[3px] border-l-[var(--success)]"
              >
                <p className="text-sm text-[var(--ink)]">
                  {formatMode(data.continueAttempt.mode)} — {data.continueAttempt.status}
                </p>
              </CardLink>
            </section>
          )}

          <section>
            <SectionLabel>Recommended</SectionLabel>
            {data.recommended.length === 0 ? (
              <EmptyState className="mt-2 text-sm text-[var(--ink-soft)]">
                No recommendations yet — browse the catalog to get started.
              </EmptyState>
            ) : (
              <div className="mt-2 space-y-2">
                {data.recommended.map((rec) => (
                  <CardLink key={rec.activityVersionId} variant="plain" href={catalogEntryRoute(rec.activityVersionId)}>
                    <p className="text-sm text-[var(--ink)]">{rec.slug}</p>
                    <p className="mt-1 text-xs text-[var(--ink-soft)]">
                      {(REASON_LABEL[rec.reasonCode] ?? (() => rec.reasonCode))(rec.reasonParams)}
                    </p>
                  </CardLink>
                ))}
              </div>
            )}
          </section>

          <section>
            <div className="flex items-baseline justify-between">
              <SectionLabel>Mastery snapshot</SectionLabel>
              <Link href="/skills" className="text-xs text-[var(--accent)] hover:text-[var(--accent-deep)]">
                View all skills →
              </Link>
            </div>
            {data.masterySnapshot.length === 0 ? (
              <EmptyState className="mt-2 text-sm text-[var(--ink-soft)]">
                No mastery evidence yet — complete an activity to see progress here.
              </EmptyState>
            ) : (
              <div className="mt-2 space-y-2">
                {data.masterySnapshot.map((skill) => (
                  <div key={skill.skill_id} className="flex items-center gap-3 text-sm">
                    <span className="w-40 shrink-0 text-[var(--ink)]">{skill.name}</span>
                    <ProgressBar
                      value={Number(skill.p_mastery)}
                      fillClassName={masteryBandFillClassName(skill.band)}
                    />
                    <span className="w-24 shrink-0 text-xs text-[var(--ink-soft)]">{skill.band}</span>
                  </div>
                ))}
              </div>
            )}
          </section>

          <div className="lms-divider-dashed pt-6">
            <Link href="/catalog" className="lms-action-btn lms-action-btn--primary">
              Browse the catalog
            </Link>
          </div>
        </div>
      )}
    </PageContainer>
  );
}
