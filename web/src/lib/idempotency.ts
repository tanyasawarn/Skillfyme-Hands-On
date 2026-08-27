/**
 * PLAN.md U3: idempotency-key builder. One real call site today
 * (`catalog/[activityVersionId]/page.tsx`'s attempt-start mutation) --
 * flagged pre-emptively in PLAN.md since every mutating endpoint's
 * contract (doc §4.4) expects one, so more call sites are expected as
 * this app grows. `scope` should uniquely identify what's being
 * deduplicated (e.g. the activity version id) -- the timestamp suffix
 * means retrying the *same* user action within the same millisecond is
 * what collapses into one request, not every call for the same scope
 * forever.
 */
export function makeIdempotencyKey(scope: string): string {
  return `web-${scope}-${Date.now()}`;
}
