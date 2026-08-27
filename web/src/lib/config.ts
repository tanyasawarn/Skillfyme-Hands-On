/**
 * PLAN.md K1: single source for the practice-core API base URL.
 * Previously independently duplicated in `api-client.ts` and
 * `auth-token.ts`, each with its own copy of the same dev-only
 * fallback -- harmless while the two literals matched, but a real
 * drift risk the moment one was edited without the other.
 */
export const API_BASE_URL = process.env.NEXT_PUBLIC_API_BASE_URL ?? 'http://localhost:3001';
