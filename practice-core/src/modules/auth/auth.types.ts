/**
 * Claims carried by the bearer JWT. Phase 1 issues these from a
 * shared-secret signer (see mint-dev-token.ts); once the app is launched
 * from the LMS, the LMS becomes the issuer but the claim shape and the
 * guard/decorator below don't need to change.
 */
export interface AuthClaims {
  userId: string;
  tenantId: string;
  role: string;
}

declare module 'express' {
  interface Request {
    auth?: AuthClaims;
  }
}
