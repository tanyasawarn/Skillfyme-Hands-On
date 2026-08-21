/**
 * Phase 1 has no auth/session layer yet -- learner identity is a stub
 * constant so the catalog/attempt UI has something concrete to call the
 * API with. Replace with real session data once an auth module exists;
 * every call site that reads these should be easy to grep for later.
 */
export const DEMO_TENANT_ID = '11111111-1111-1111-1111-111111111111';
export const DEMO_USER_ID = '55555555-5555-5555-5555-555555555555';

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
