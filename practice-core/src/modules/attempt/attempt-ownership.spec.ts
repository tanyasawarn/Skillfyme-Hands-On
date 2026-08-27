import { isOwnedByCaller } from './attempt-ownership';

/**
 * PLAN.md Phase 3's S8, security-flagged per the plan's own note (line
 * 315: "S8 and K9 touch auth -- get a second reviewer"). Tests the pure
 * decision function directly (see attempt-ownership.ts's own doc
 * comment for why -- AttemptOwnershipGuard itself pulls in
 * AttemptRepository -> kysely, which this project's Jest config cannot
 * load). Doc §9.1 T3 names cross-learner data access as exactly the
 * risk this function closes.
 */
describe('isOwnedByCaller', () => {
  const OWNER = { userId: 'user-a', tenantId: 'tenant-a' };
  const ATTEMPT = { user_id: 'user-a', tenant_id: 'tenant-a' };

  it('returns true when the caller genuinely owns the attempt', () => {
    expect(isOwnedByCaller(ATTEMPT, OWNER)).toBe(true);
  });

  it('SECURITY: returns false for a different user_id in the same tenant', () => {
    const attacker = { userId: 'user-b', tenantId: 'tenant-a' };
    expect(isOwnedByCaller(ATTEMPT, attacker)).toBe(false);
  });

  it('SECURITY: returns false for a matching user_id in a DIFFERENT tenant (cross-tenant isolation)', () => {
    const crossTenant = { userId: 'user-a', tenantId: 'tenant-b' };
    expect(isOwnedByCaller(ATTEMPT, crossTenant)).toBe(false);
  });

  it('SECURITY: returns false when both user_id and tenant_id differ', () => {
    const totalStranger = { userId: 'user-z', tenantId: 'tenant-z' };
    expect(isOwnedByCaller(ATTEMPT, totalStranger)).toBe(false);
  });

  it('SECURITY: returns false when the attempt is undefined (does not exist)', () => {
    expect(isOwnedByCaller(undefined, OWNER)).toBe(false);
  });

  it('SECURITY: returns false when the caller is undefined (no auth claims resolved)', () => {
    expect(isOwnedByCaller(ATTEMPT, undefined)).toBe(false);
  });

  it('SECURITY: returns false when both attempt and caller are undefined', () => {
    expect(isOwnedByCaller(undefined, undefined)).toBe(false);
  });

  it('is case-sensitive on both id fields (no accidental loose matching)', () => {
    const wrongCase = { userId: 'USER-A', tenantId: 'tenant-a' };
    expect(isOwnedByCaller(ATTEMPT, wrongCase)).toBe(false);
  });
});
