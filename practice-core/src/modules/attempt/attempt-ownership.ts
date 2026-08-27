/**
 * Pure ownership-decision logic for AttemptOwnershipGuard (PLAN.md
 * Phase 3's S8), split into its own dependency-free module for the same
 * reason cooldown.ts/criteria.ts already are: this project's unit jest
 * config has no `transformIgnorePatterns` override for kysely's ESM
 * build, so any file importing AttemptRepository (even just for its
 * type) transitively pulls in kysely and fails to load under Jest.
 * Keeping the actual security decision here, with zero imports, makes
 * it directly unit-testable without a database or a mocked repository.
 */
export interface AttemptOwnerRecord {
  user_id: string;
  tenant_id: string;
}

export interface CallerClaims {
  userId: string;
  tenantId: string;
}

/**
 * Doc §9.1 T3 ("cross-learner data access"): true only if attempt is a
 * real record AND both its user_id and tenant_id match the caller's own
 * claims. A missing attempt and a real-but-not-owned attempt both
 * resolve to false, deliberately indistinguishable to the caller (the
 * guard throws the same ForbiddenException either way) -- a 404-vs-403
 * distinction here would leak "this attempt id exists" to a caller who
 * has no right to know that.
 */
export function isOwnedByCaller(
  attempt: AttemptOwnerRecord | undefined,
  caller: CallerClaims | undefined,
): boolean {
  return (
    !!attempt &&
    !!caller &&
    attempt.user_id === caller.userId &&
    attempt.tenant_id === caller.tenantId
  );
}
