/**
 * PLAN.md K3: the `course` URL query-param key the external LMS uses to
 * tell this app which curriculum to show on launch. Previously
 * hardcoded as the bare string `'course'` independently in 3 files
 * (`catalog/page.tsx`, `skills/page.tsx`, `Sidebar.tsx`) with no shared
 * source -- a typo in any one of them would silently stop that page
 * from reading the LMS-provided course.
 */
export const COURSE_QUERY_PARAM = 'course';
