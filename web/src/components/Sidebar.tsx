'use client';

import { Suspense } from 'react';
import Link from 'next/link';
import { usePathname, useRouter, useSearchParams } from 'next/navigation';
import { DEFAULT_COURSE_SLUG } from '@/lib/demo-context';

const NAV_ITEMS = [
  { href: '/catalog', label: 'Catalog' },
  { href: '/skills', label: 'Skills' },
  { href: '/history', label: 'History' },
];

const COURSES = [
  { slug: 'devops-with-ai', label: 'DevOps With AI' },
  { slug: 'genai-with-ml', label: 'Generative AI With ML' },
];

/**
 * Left rail nav (design system spec: 256px, sticky). Replaces the topnav
 * Catalog/Skills links -- the topnav now carries only the brand mark and
 * the profile menu, matching a conventional app shell (persistent left
 * nav + slim top bar) rather than everything crammed into one bar.
 */
export function Sidebar() {
  return (
    <aside className="sidebar">
      <Suspense fallback={<SidebarContent course={null} />}>
        <CourseAwareSidebar />
      </Suspense>
    </aside>
  );
}

/**
 * The `course` URL param is how the external LMS tells this app which
 * curriculum to show on launch. That's still the source of truth when a
 * learner arrives from the LMS -- this switcher is an in-app override for
 * exploring the other course without going back to the LMS. Switching
 * updates the URL param (so Catalog/Skills navigation keeps following it,
 * same as before) rather than introducing separate app state.
 */
function CourseAwareSidebar() {
  const course = useSearchParams().get('course');
  return <SidebarContent course={course} />;
}

function SidebarContent({ course }: { course: string | null }) {
  const router = useRouter();
  const pathname = usePathname();
  const activeCourse = course ?? DEFAULT_COURSE_SLUG;
  const suffix = course ? `?course=${encodeURIComponent(course)}` : '';

  function handleCourseChange(nextSlug: string) {
    router.push(`${pathname}?course=${encodeURIComponent(nextSlug)}`);
  }

  return (
    <>
      <div className="sidebar-course-switcher">
        <label htmlFor="course-switcher" className="font-mono-label">
          Course
        </label>
        <select
          id="course-switcher"
          className="course-select"
          value={activeCourse}
          onChange={(e) => handleCourseChange(e.target.value)}
        >
          {COURSES.map((c) => (
            <option key={c.slug} value={c.slug}>
              {c.label}
            </option>
          ))}
        </select>
      </div>

      <nav className="sidebar-nav">
        {NAV_ITEMS.map((item) => {
          const active = pathname === item.href || pathname.startsWith(`${item.href}/`);
          return (
            <Link
              key={item.href}
              href={`${item.href}${suffix}`}
              className={`sidebar-link${active ? ' sidebar-link--active' : ''}`}
            >
              {item.label}
            </Link>
          );
        })}
      </nav>
    </>
  );
}
