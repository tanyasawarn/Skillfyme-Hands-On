'use client';

import { Suspense } from 'react';
import { useSearchParams } from 'next/navigation';
import { useQuery } from '@tanstack/react-query';
import { api } from '@/lib/api-client';
import { DEFAULT_COURSE_SLUG, DEMO_USER_ID } from '@/lib/demo-context';

const BAND_COLOR: Record<string, string> = {
  Novice: 'lms-pill--muted',
  Developing: 'lms-pill--warning',
  Competent: 'lms-pill--accent',
  Proficient: 'lms-pill--success',
  Mastered: 'lms-pill--success',
};

const COURSE_TITLE: Record<string, string> = {
  'devops-with-ai': 'DevOps With AI',
  'genai-with-ml': 'Generative AI With ML',
};

export default function SkillsPage() {
  return (
    <Suspense fallback={<div className="mx-auto max-w-3xl px-6 py-10 text-[var(--ink-muted)]">Loading…</div>}>
      <SkillsPageInner />
    </Suspense>
  );
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
  const courseSlug = searchParams.get('course') ?? DEFAULT_COURSE_SLUG;
  const courseTitle = COURSE_TITLE[courseSlug] ?? courseSlug;

  const { data, isLoading } = useQuery({
    queryKey: ['skills', DEMO_USER_ID, courseSlug],
    queryFn: () => api.getSkills(DEMO_USER_ID, courseSlug),
  });

  return (
    <div className="mx-auto max-w-3xl px-6 py-10">
      <h1 className="font-display text-2xl font-extrabold">{courseTitle} — Skills</h1>
      <p className="mt-1 text-sm text-[var(--ink-muted)]">
        Doc §2.4: mastery bands and evidence counts, not raw probabilities.
      </p>

      {isLoading && <p className="mt-8 text-[var(--ink-muted)]">Loading…</p>}

      {data && data.length === 0 && (
        <p className="mt-8 text-[var(--ink-soft)]">
          No mastery evidence yet — complete a lab to start building your skill profile.
        </p>
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
                  {(Number(skill.p_mastery) * 100).toFixed(0)}%
                </span>
                <span className={`lms-pill ${BAND_COLOR[skill.band]}`}>{skill.band}</span>
              </div>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
