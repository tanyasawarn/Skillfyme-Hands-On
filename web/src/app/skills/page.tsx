'use client';

import { useSearchParams } from 'next/navigation';
import { useQuery } from '@tanstack/react-query';
import { api } from '@/lib/api-client';
import { DEFAULT_COURSE_SLUG } from '@/lib/demo-context';
import { useSession } from '@/lib/session';
import { Loader } from '@/components/ui/Loader';
import { Badge } from '@/components/ui/Badge';
import { PageContainer } from '@/components/ui/PageContainer';
import { EmptyState } from '@/components/ui/EmptyState';
import { WithSearchParamsSuspense } from '@/components/ui/WithSearchParamsSuspense';
import { formatPercent } from '@/lib/format';
import { courseLabel } from '@/lib/courses';
import { COURSE_QUERY_PARAM } from '@/lib/route-params';
import { masteryBandVariant } from '@/lib/mastery';

export default function SkillsPage() {
  return <WithSearchParamsSuspense Component={SkillsPageInner} loadingLabel="Loading…" />;
}

/**
 * Doc §1.2: "Skills → skill graph view, mastery per skill, 'practice
 * this skill'." Phase 1 scope: a flat list with mastery bands and
 * evidence counts -- the doc's graph-view visualisation is a richer UI
 * investment than Dev B's Phase 1 slice covers; band/evidence display
 * matches §2.4's explicit guidance ("displaying bands and evidence,
 * never raw probabilities").
 *
 * `course` comes from the same URL param the catalog page reads (set by
 * the external LMS on launch) -- without it, a learner with mastery
 * evidence in both DevOps-with-AI and GenAI-with-ML would see both
 * courses' skills mixed into one list with no course label.
 */
function SkillsPageInner() {
  const searchParams = useSearchParams();
  const courseSlug = searchParams.get(COURSE_QUERY_PARAM) ?? DEFAULT_COURSE_SLUG;
  const courseTitle = courseLabel(courseSlug);

  const session = useSession();
  const { data, isLoading } = useQuery({
    queryKey: ['skills', session?.userId, courseSlug],
    queryFn: () => api.getSkills(session!.userId, courseSlug),
    enabled: !!session,
  });

  return (
    <PageContainer spacing="py-10">
      <h1 className="font-display text-2xl font-extrabold">{courseTitle} — Skills</h1>
      <p className="mt-1 text-sm text-[var(--ink-muted)]">
        Doc §2.4: mastery bands and evidence counts, not raw probabilities.
      </p>

      {(isLoading || !session) && <Loader label="Loading…" />}

      {data && data.length === 0 && (
        <EmptyState>
          No mastery evidence yet — complete a lab to start building your skill profile.
        </EmptyState>
      )}

      {data && data.length > 0 && (
        <ul className="mt-8 divide-y divide-[var(--border)] rounded-lg border border-[var(--border)] bg-[var(--surface)]">
          {data.map((skill) => (
            <li key={skill.skill_id} className="flex items-center justify-between gap-4 p-4">
              <div>
                <p className="text-sm text-[var(--ink)]">{skill.name}</p>
                <p className="mt-0.5 text-xs text-[var(--ink-soft)]">
                  {skill.domain} · {skill.evidence_count} evidence point{skill.evidence_count === 1 ? '' : 's'}
                </p>
              </div>
              <div className="flex items-center gap-3">
                <span className="font-mono text-xs text-[var(--ink-soft)]">
                  {formatPercent(Number(skill.p_mastery))}
                </span>
                <Badge variant={masteryBandVariant(skill.band)}>{skill.band}</Badge>
              </div>
            </li>
          ))}
        </ul>
      )}
    </PageContainer>
  );
}
