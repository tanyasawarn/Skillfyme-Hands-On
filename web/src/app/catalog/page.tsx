'use client';

import { useMemo } from 'react';
import { useSearchParams } from 'next/navigation';
import { useQuery } from '@tanstack/react-query';
import { api, type SkillCatalogEntry } from '@/lib/api-client';
import { DEFAULT_COURSE_SLUG } from '@/lib/demo-context';
import { useSession } from '@/lib/session';
import { courseLabel } from '@/lib/courses';
import { COURSE_QUERY_PARAM } from '@/lib/route-params';
import { Badge } from '@/components/ui/Badge';
import { PageContainer } from '@/components/ui/PageContainer';
import { EmptyState } from '@/components/ui/EmptyState';
import { CardLink } from '@/components/ui/CardLink';
import { QueryBoundary } from '@/components/ui/QueryBoundary';
import { WithSearchParamsSuspense } from '@/components/ui/WithSearchParamsSuspense';
import { catalogEntryRoute } from '@/lib/routes';
import { SectionLabel } from '@/components/ui/SectionLabel';
import { formatCurrency } from '@/lib/format';

const DIFFICULTY_LABEL: Record<string, string> = {
  L1: 'L1 · Guided',
  L2: 'L2 · Assisted',
  L3: 'L3 · Independent',
  L4: 'L4 · Production',
  L5: 'L5 · Expert',
};

// Domain labels are keyed per-course since each course's domain slugs
// are independent (both use short slugs like the DevOps track's "core"
// vs GenAI's "python-stats" -- no collision, but no shared vocabulary
// either).
const DOMAIN_LABEL: Record<string, Record<string, string>> = {
  'devops-with-ai': {
    core: 'DevOps Core Concepts',
    cicd: 'CI/CD Tools',
    containers: 'Containerisation (Docker & Kubernetes)',
    cloud: 'Cloud',
    iac: 'Infrastructure as Code',
    observability: 'Monitoring, Logging & Observability',
    aiops: 'DevOps with AI',
  },
  'genai-with-ml': {
    'python-stats': 'Python & Statistics Essentials',
    'applied-ml': 'Applied Machine Learning',
    'deep-learning': 'Deep Learning & Neural Network Architectures',
    nlp: 'Natural Language Processing (NLP)',
    'genai-essentials': 'Generative AI Essentials & Prompt Engineering',
    'llm-apps': 'LLM-based Application Development',
    'agentic-ai': 'Agentic AI & LLMOps for Scalable AI Systems',
  },
};

// One SVG glyph per curriculum module -- stroke="currentColor" so each
// inherits the accent color from its wrapper, matching the design
// system's warm-cool palette instead of introducing new colors. One flat
// map covering both courses: domain slugs never collide across courses
// (DevOps uses core/cicd/containers/..., GenAI uses python-stats/
// applied-ml/...), so no per-course nesting needed here unlike labels.
const DOMAIN_ICON: Record<string, React.ReactNode> = {
  core: (
    <path d="M12 2 3 7l9 5 9-5-9-5Zm-9 10 9 5 9-5M3 17l9 5 9-5" />
  ),
  cicd: (
    <path d="M12 2v4m0 12v4M4.9 4.9l2.8 2.8m8.6 8.6 2.8 2.8M2 12h4m12 0h4M4.9 19.1l2.8-2.8m8.6-8.6 2.8-2.8" />
  ),
  containers: (
    <path d="M21 8 12 3 3 8l9 5 9-5Zm0 0v8l-9 5m0-13v13m0-13L3 8m0 0v8l9 5" />
  ),
  cloud: (
    <path d="M6.5 19a4.5 4.5 0 0 1-.5-8.98A6 6 0 0 1 17.6 8.05 4.5 4.5 0 0 1 17 19H6.5Z" />
  ),
  iac: (
    <path d="M4 4h16v4H4zm0 6h16v4H4zm0 6h10v4H4z" />
  ),
  observability: (
    <path d="M3 12s3-7 9-7 9 7 9 7-3 7-9 7-9-7-9-7Z M12 15a3 3 0 1 0 0-6 3 3 0 0 0 0 6Z" />
  ),
  aiops: (
    <path d="M12 2a4 4 0 0 1 4 4c0 1.5-.8 2.5-1.5 3.3-.5.6-.5 1-.5 1.7v1H10v-1c0-.7 0-1.1-.5-1.7C8.8 8.5 8 7.5 8 6a4 4 0 0 1 4-4ZM9 15h6M10 18h4" />
  ),
  // GenAI With ML domain icons
  'python-stats': (
    <path d="M9 3H7a4 4 0 0 0-4 4v2a3 3 0 0 1-3 3 3 3 0 0 1 3 3v2a4 4 0 0 0 4 4h2M15 3h2a4 4 0 0 1 4 4v2a3 3 0 0 0 3 3 3 3 0 0 0-3 3v2a4 4 0 0 1-4 4h-2" />
  ),
  'applied-ml': (
    <path d="M4 19V5m4 14V9m4 10V13m4 6V7m4 12V11" />
  ),
  'deep-learning': (
    <path d="M12 3a4 4 0 0 0-4 4v1a3 3 0 0 0-2 2.8 3 3 0 0 0 2 2.8V15a4 4 0 0 0 4 4 4 4 0 0 0 4-4v-1.4a3 3 0 0 0 2-2.8 3 3 0 0 0-2-2.8V7a4 4 0 0 0-4-4Z M9 12h6" />
  ),
  nlp: (
    <path d="M4 5h16M4 5v10a2 2 0 0 0 2 2h4l3 3v-3h5a2 2 0 0 0 2-2V5" />
  ),
  'genai-essentials': (
    <path d="M12 2v3m0 14v3M4.2 4.2l2.1 2.1m11.4 11.4 2.1 2.1M2 12h3m14 0h3M4.2 19.8l2.1-2.1m11.4-11.4 2.1-2.1M12 8a4 4 0 1 0 0 8 4 4 0 0 0 0-8Z" />
  ),
  'llm-apps': (
    <path d="M4 4h16v12H8l-4 4V4Z" />
  ),
  'agentic-ai': (
    <path d="M12 3v3m-5.2.8L9 8.8M3 12h3m.8 5.2L8.8 15M12 21v-3m5.2-.8L14.2 15m5.8-3h-3m-.8-5.2L13.2 8.8M12 9a3 3 0 1 0 0 6 3 3 0 0 0 0-6Z" />
  ),
};

function DomainIcon({ domain }: { domain: string }) {
  return (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.75" strokeLinecap="round" strokeLinejoin="round" width="20" height="20">
      {DOMAIN_ICON[domain] ?? <circle cx="12" cy="12" r="8" />}
    </svg>
  );
}

/**
 * Skill-driven catalog: the curriculum's skill graph is the entry point,
 * not the (currently near-empty) activity list. Each skill card is a
 * trigger for its mapped Guided Lab -- clicking it lands on the exact
 * same activity-detail -> start-attempt flow that already exists
 * (catalog/[activityVersionId]/page.tsx), unchanged. A skill with no
 * lab authored for it yet renders locked rather than being hidden, so
 * the catalog honestly shows the full curriculum scope even while most
 * of it has no content behind it yet.
 */
export default function CatalogPage() {
  return (
    <WithSearchParamsSuspense Component={CatalogPageInner} loadingLabel="Loading catalog…" maxWidth="6xl" />
  );
}

/**
 * useSearchParams() opts this subtree into client-side rendering during
 * prerender (Next.js requirement) -- split out from the default export so
 * only this inner component needs the Suspense boundary, not anything
 * that might wrap CatalogPage later.
 */
function CatalogPageInner() {
  // `course` comes from the external LMS's launch URL (this app is opened
  // per-course; there is no in-app switcher). Falls back to the original
  // DevOps track when absent, so local dev / any caller not yet updated
  // to pass it keeps working.
  const searchParams = useSearchParams();
  const courseSlug = searchParams.get(COURSE_QUERY_PARAM) ?? DEFAULT_COURSE_SLUG;
  const courseTitle = courseLabel(courseSlug);
  const domainLabels = DOMAIN_LABEL[courseSlug] ?? {};

  const session = useSession();
  const { data, isLoading, isError, error } = useQuery({
    queryKey: ['skill-catalog', session?.tenantId, courseSlug],
    queryFn: () => api.listSkillCatalog(session!.tenantId, courseSlug),
    enabled: !!session,
  });

  const domains = useMemo(() => {
    if (!data) return [];
    // Preserve API order (curriculum teaching order via domain_order/
    // sequence, doc §2.1) -- do NOT re-sort domains alphabetically here,
    // that's exactly the bug that put "DevOps with AI" above "DevOps Core
    // Concepts" ('aiops' < 'core' as text).
    const byDomain = new Map<string, SkillCatalogEntry[]>();
    for (const entry of data) {
      const list = byDomain.get(entry.skill_domain) ?? [];
      list.push(entry);
      byDomain.set(entry.skill_domain, list);
    }
    return [...byDomain.entries()];
  }, [data]);

  const availableCount = data?.filter((e) => e.activity_id).length ?? 0;

  return (
    <PageContainer maxWidth="6xl" spacing="py-10">
      <div className="catalog-hero">
        <div>
          <h1 className="font-display text-2xl font-extrabold">{courseTitle} — Practice Catalog</h1>
          <p className="mt-1 max-w-xl text-sm text-[var(--ink-muted)]">
            Every skill in the {courseTitle} curriculum is a practice entry point. Pick a skill,
            and we provision a real hands-on lab for it.
          </p>
        </div>
        {data && data.length > 0 && (
          <div className="catalog-hero-stat">
            <span className="catalog-hero-stat-value">{availableCount}</span>
            <span className="catalog-hero-stat-label">of {data.length} labs live</span>
          </div>
        )}
      </div>

      <QueryBoundary isLoading={isLoading || !session} isError={isError} error={error} loadingLabel="Loading catalog…">
      {data && data.length === 0 && (
        <EmptyState>
          No skills loaded yet. Run scripts/seed-skills.ts to load the curriculum skill graph.
        </EmptyState>
      )}

      {data && data.length > 0 && (
        <div className="mt-10 space-y-12">
          {domains.map(([domain, skills]) => {
            const domainAvailable = skills.filter((s) => s.activity_id).length;
            return (
              <section key={domain}>
                <div className="domain-header">
                  <div className="domain-icon">
                    <DomainIcon domain={domain} />
                  </div>
                  <div className="flex-1">
                    <h2 className="font-display text-base font-bold text-[var(--ink)]">
                      {domainLabels[domain] ?? domain}
                    </h2>
                    <SectionLabel as="p" className="mt-0.5">
                      {domainAvailable > 0 ? `${domainAvailable} lab${domainAvailable > 1 ? 's' : ''} live · ` : ''}
                      {skills.length} skills
                    </SectionLabel>
                  </div>
                </div>
                <div className="skill-grid">
                  {skills.map((entry) => (
                    <SkillCard key={entry.skill_id} entry={entry} />
                  ))}
                </div>
              </section>
            );
          })}
        </div>
      )}
      </QueryBoundary>
    </PageContainer>
  );
}

function SkillCard({ entry }: { entry: SkillCatalogEntry }) {
  const available = !!entry.activity_id && !!entry.activity_version_id;

  const body = (
    <>
      <div className="skill-card-top">
        <Badge
          variant={available ? 'success' : 'outline'}
          icon={
            available ? (
              <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" width="13" height="13">
                <path d="M5 12l5 5L20 7" />
              </svg>
            ) : (
              <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" width="13" height="13">
                <rect x="5" y="11" width="14" height="9" rx="2" />
                <path d="M8 11V8a4 4 0 0 1 8 0v3" />
              </svg>
            )
          }
        >
          {available ? 'Live' : 'Locked'}
        </Badge>
        {available && entry.difficulty_level && (
          <Badge variant="accent">{DIFFICULTY_LABEL[entry.difficulty_level]}</Badge>
        )}
      </div>

      <p className={`skill-card-title ${available ? '' : 'skill-card-title--locked'}`}>{entry.skill_name}</p>

      <p className="skill-card-meta">
        {available
          ? `${entry.activity_slug} · v${entry.activity_version}`
          : 'No lab authored yet'}
      </p>

      {available && (
        <div className="skill-card-footer">
          <span>
            {entry.estimated_minutes ? `~${entry.estimated_minutes} min` : ''}
            {entry.cost_budget_usd ? ` · ${formatCurrency(Number(entry.cost_budget_usd))}` : ''}
          </span>
          <span className="skill-card-cta">
            Start
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.5" strokeLinecap="round" strokeLinejoin="round" width="13" height="13">
              <path d="M5 12h14M13 6l6 6-6 6" />
            </svg>
          </span>
        </div>
      )}
    </>
  );

  return (
    <CardLink
      variant="lift"
      href={available && entry.activity_version_id ? catalogEntryRoute(entry.activity_version_id) : '#'}
      disabled={!available}
    >
      {body}
    </CardLink>
  );
}
