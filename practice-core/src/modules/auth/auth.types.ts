import type { Role } from './role';

/**
 * Claims carried by the bearer JWT. Phase 1 issues these from a
 * shared-secret signer (see mint-dev-token.ts); once the app is launched
 * from the LMS, the LMS becomes the issuer but the claim shape and the
 * guard/decorator below don't need to change.
 *
 * `role: Role` here is a compile-time claim about a value that has
 * crossed a trust boundary (a decoded JWT payload) -- it is only true
 * because `AuthGuard.canActivate()` runs `isRole()` on the raw decoded
 * string before ever constructing a `req.auth` value, not because
 * `jwt.verify<AuthClaims>()` itself validates anything (a generic type
 * parameter on `verify()` is a cast, not a runtime check -- see
 * `role.ts`'s own doc comment on why an unvalidated cast here would
 * silently defeat the whole point of narrowing this field to `Role`).
 */
export interface AuthClaims {
  userId: string;
  tenantId: string;
  role: Role;
}

declare module 'express' {
  interface Request {
    auth?: AuthClaims;
  }
}
