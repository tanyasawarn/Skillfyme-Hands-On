'use client';

import { Suspense } from 'react';
import Link from 'next/link';
import { usePathname, useRouter, useSearchParams } from 'next/navigation';
import { DEFAULT_COURSE_SLUG } from '@/lib/demo-context';
import { COURSES } from '@/lib/courses';
import { COURSE_QUERY_PARAM } from '@/lib/route-params';
import { SectionLabel } from '@/components/ui/SectionLabel';

const NAV_ITEMS = [
  { href: '/catalog', label: 'Catalog' },
  { href: '/skills', label: 'Skills' },
  { href: '/history', label: 'History' },
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
  const course = useSearchParams().get(COURSE_QUERY_PARAM);
  return <SidebarContent course={course} />;
}

function SidebarContent({ course }: { course: string | null }) {
  const router = useRouter();
  const pathname = usePathname();
  const activeCourse = course ?? DEFAULT_COURSE_SLUG;
  const suffix = course ? `?${COURSE_QUERY_PARAM}=${encodeURIComponent(course)}` : '';

  function handleCourseChange(nextSlug: string) {
    router.push(`${pathname}?${COURSE_QUERY_PARAM}=${encodeURIComponent(nextSlug)}`);
  }

  return (
    <>
      <div className="sidebar-course-switcher">
        <SectionLabel as="label" htmlFor="course-switcher">
          Course
        </SectionLabel>
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
