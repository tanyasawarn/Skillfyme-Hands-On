/**
 * PLAN.md K4: course slug -> label, previously triplicated
 * independently across `catalog/page.tsx`'s `COURSE_TITLE`,
 * `skills/page.tsx`'s `COURSE_TITLE`, and `Sidebar.tsx`'s `COURSES`
 * array -- three copies that would silently drift the moment a 3rd
 * course ships and only one of the three is updated.
 */
export interface Course {
  slug: string;
  label: string;
}

export const COURSES: readonly Course[] = [
  { slug: 'devops-with-ai', label: 'DevOps With AI' },
  { slug: 'genai-with-ml', label: 'Generative AI With ML' },
];

const COURSE_LABEL_BY_SLUG: Record<string, string> = Object.fromEntries(
  COURSES.map((c) => [c.slug, c.label]),
);

/** Falls back to the raw slug for a course not in the known list (e.g. a not-yet-authored course). */
export function courseLabel(slug: string): string {
  return COURSE_LABEL_BY_SLUG[slug] ?? slug;
}
