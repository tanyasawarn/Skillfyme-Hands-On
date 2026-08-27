import { ApiError } from './api-client';
import { API_BASE_URL } from './config';

/**
 * Phase 3 standardization: the actual duplication across catalog/page.tsx
 * and history/page.tsx wasn't styling (Alert.tsx handles that) -- it was
 * this decision logic, copy-pasted identically in both: "assume any
 * fetch failure means the API is unreachable, show a fixed troubleshoot
 * message plus String(error)." That assumption is wrong for the common
 * case now that the backend has a real global exception filter
 * (practice-core/src/common/all-exceptions.filter.ts) returning
 * structured { statusCode, message } bodies for real API errors (404s,
 * 403s, validation failures) -- those deserve their own real message
 * shown to the user, not a generic "is the server running?" banner.
 *
 * Distinguishes three cases:
 *   - ApiError (a real HTTP response reached us): show its message
 *     field, which is now guaranteed meaningful by the backend's
 *     exception filter.
 *   - a network-level failure (fetch itself threw, e.g. connection
 *     refused -- the actual "is practice-core running?" case): the
 *     one case where the old banners' assumption was correct.
 *   - anything else (a bug in this code, not the API call): a generic
 *     fallback, same "never blindly stringify and show" caution as the
 *     backend filter's own non-HttpException path.
 */
export interface UserFacingError {
  headline: string;
  detail?: string;
}

export function toUserFacingError(error: unknown): UserFacingError {
  if (error instanceof ApiError) {
    const body = error.body as { message?: unknown } | undefined;
    const message = typeof body?.message === 'string' ? body.message : error.message;
    return { headline: message };
  }

  if (error instanceof TypeError && /fetch/i.test(error.message)) {
    // fetch() throws a TypeError ("Failed to fetch" / "fetch failed")
    // on network-level failure (DNS, connection refused, CORS block) --
    // this is the one case the old duplicated banners were actually
    // built for.
    return {
      headline: `Could not reach practice-core at ${API_BASE_URL}. Is it running (\`npm run start:dev\` in /practice-core)?`,
      detail: error.message,
    };
  }

  return {
    headline: 'Something went wrong.',
    detail: error instanceof Error ? error.message : String(error),
  };
}
