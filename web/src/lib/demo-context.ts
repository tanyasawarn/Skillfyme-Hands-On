/**
 * Learner identity is no longer a constant here -- it comes from the
 * session JWT via `lib/session.ts` (`useSession()`), so the app calls
 * the API as whoever the token represents. See
 * PHASE1_MVP_COMPLETION.md §1.1 / §5.
 *
 * Only the course slug remains, because it is genuinely not identity:
 * the LMS passes it per-launch as a URL param, not something carried in
 * the token.
 */

/**
 * This app is launched per-course from an external LMS (the LMS handles
 * enrollment; a learner clicks "Practice" on a course they're already
 * enrolled in, which opens this app in a new tab). The LMS passes which
 * course via a `course` URL query param -- there is no in-app course
 * switcher. Falls back to the original DevOps track so the app still
 * works when opened without the param (local dev, or any caller that
 * hasn't been updated to pass it yet).
 */
export const DEFAULT_COURSE_SLUG = 'devops-with-ai';
