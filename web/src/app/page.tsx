'use client';

import Link from 'next/link';
import { useQuery } from '@tanstack/react-query';
import { api } from '@/lib/api-client';
import { DEMO_TENANT_ID, DEMO_USER_ID } from '@/lib/demo-context';

const BAND_COLOR: Record<string, string> = {
  Novice: 'bg-[var(--ink-soft)]',
  Developing: 'bg-[var(--warning)]',
  Competent: 'bg-[var(--accent)]',
  Proficient: 'bg-[var(--success)]',
  Mastered: 'bg-[var(--success)]',
};

const REASON_LABEL: Record<string, (params: Record<string, unknown>) => string> = {
  CURRICULUM_ADJACENT: (p) => `Related to ${p.topic ?? 'your current topic'}`,
  REMEDIATION: (p) =>
    `Recommended because your mastery of this skill is ${p.mastery !== undefined ? `${(Number(p.mastery) * 100).toFixed(0)}%` : 'low'}`,
};

/**
 * Doc §1.2: "Home is a decision surface, not a dashboard. Its job is to
 * make the next click obvious. Maximum three primary CTAs: Continue
 * attempt, Recommended next, Fix a weak skill." This is that page, now
 * that RecommendationService (M1.11) and mastery listing (rest of M1.13)
 * both have real data behind them.
 */
export default function Home() {
  const { data, isLoading } = useQuery({
    queryKey: ['dashboard', DEMO_USER_ID],
    queryFn: () => api.getDashboard(DEMO_USER_ID, DEMO_TENANT_ID),
  });

  return (
    <div className="mx-auto max-w-3xl px-6 py-12">
      <h1 className="font-display text-2xl font-extrabold">Practice Engine</h1>
      <p className="mt-1 text-sm text-[var(--ink-muted)]">
        Learn → Follow → Implement → Troubleshoot → Design → Build → Defend
      </p>

      {isLoading && <p className="mt-10 text-[var(--ink-muted)]">Loading…</p>}

      {data && (
        <div className="mt-10 space-y-8">
          {data.continueAttempt && (
            <section>
              <h2 className="font-mono-label">Continue</h2>
              <Link
                href={`/attempts/${data.continueAttempt.id}`}
                className="lms-card mt-2 block p-4 border-l-[3px] border-l-[var(--success)]"
              >
                <p className="text-sm text-[var(--ink)]">
                  {data.continueAttempt.mode.replace('_', ' ')} — {data.continueAttempt.status}
                </p>
              </Link>
            </section>
          )}

          <section>
            <h2 className="font-mono-label">Recommended</h2>
            {data.recommended.length === 0 ? (
              <p className="mt-2 text-sm text-[var(--ink-soft)]">No recommendations yet — browse the catalog to get started.</p>
            ) : (
              <div className="mt-2 space-y-2">
                {data.recommended.map((rec) => (
                  <Link
                    key={rec.activityVersionId}
                    href={`/catalog/${rec.activityVersionId}`}
                    className="lms-card block p-4"
                  >
                    <p className="text-sm text-[var(--ink)]">{rec.slug}</p>
                    <p className="mt-1 text-xs text-[var(--ink-soft)]">
                      {(REASON_LABEL[rec.reasonCode] ?? (() => rec.reasonCode))(rec.reasonParams)}
                    </p>
                  </Link>
                ))}
              </div>
            )}
          </section>

          <section>
            <div className="flex items-baseline justify-between">
              <h2 className="font-mono-label">Mastery snapshot</h2>
              <Link href="/skills" className="text-xs text-[var(--accent)] hover:text-[var(--accent-deep)]">
                View all skills →
              </Link>
            </div>
            {data.masterySnapshot.length === 0 ? (
              <p className="mt-2 text-sm text-[var(--ink-soft)]">No mastery evidence yet — complete an activity to see progress here.</p>
            ) : (
              <div className="mt-2 space-y-2">
                {data.masterySnapshot.map((skill) => (
                  <div key={skill.skill_id} className="flex items-center gap-3 text-sm">
                    <span className="w-40 shrink-0 text-[var(--ink)]">{skill.name}</span>
                    <div className="h-1.5 flex-1 overflow-hidden rounded-full bg-[var(--inset)]">
                      <div
                        className={`h-full ${BAND_COLOR[skill.band] ?? 'bg-[var(--ink-soft)]'}`}
                        style={{ width: `${Number(skill.p_mastery) * 100}%` }}
                      />
                    </div>
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
    </div>
  );
}
