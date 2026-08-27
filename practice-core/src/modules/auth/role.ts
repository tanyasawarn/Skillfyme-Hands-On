/**
 * PLAN.md Phase 3's K9: `Role`, replacing the untyped `role: string` on
 * `AuthClaims` and the untyped `...roles: string[]` on `@Roles()` --
 * **security-adjacent, PLAN.md's own second-reviewer note** (line 315),
 * since a role check with no compile-time constraint on its own inputs
 * can silently no-op. Confirmed live before this fix: `@Roles('admni')`
 * (a typo) compiled clean and made the guarded route unreachable by any
 * real role -- `RolesGuard.canActivate()`'s `required.includes(req.auth.role)`
 * is never true for a role that doesn't exist, which fails closed for an
 * accidental typo on a real route (denies everyone, including legitimate
 * admins) but is exactly the class of bug that becomes fail-open the
 * moment someone "fixes" it by loosening the check instead of fixing the
 * typo. A typo on `AuthController`'s dev-login `role` request body is a
 * sharper version of the same risk: that endpoint mints a real signed JWT
 * from a caller-supplied string with zero prior validation.
 *
 * Only 3 real values exist anywhere in this codebase today (DB default
 * `'learner'` in db/migrations/0001_curriculum_and_skills.sql,
 * `AdminController`'s class-level `'admin'`/`'author'` gate and its
 * method-level `'admin'`-only override) -- confirmed via a full grep
 * across src/ before choosing this list, not assumed.
 */
export enum Role {
  LEARNER = 'learner',
  AUTHOR = 'author',
  ADMIN = 'admin',
}

export const ROLE_VALUES: readonly Role[] = Object.values(Role);

export function isRole(value: string): value is Role {
  return (ROLE_VALUES as readonly string[]).includes(value);
}
