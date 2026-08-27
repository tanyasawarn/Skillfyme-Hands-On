/**
 * Learner identity, derived from the session JWT rather than hardcoded.
 *
 * PHASE1_MVP_COMPLETION.md §1.1 / §5: `DEMO_USER_ID` / `DEMO_TENANT_ID`
 * (lib/demo-context.ts) were imported directly by every page as the
 * identity to call the API with. That is a bypass -- the app ignored
 * whoever the token actually represents. This module reads `userId` and
 * `tenantId` out of the bearer token that `auth-token.ts` already
 * obtains and caches, so identity now flows from the token exactly as it
 * will once the token comes from a real LTI launch instead of
 * `POST /v1/auth/dev-login`.
 *
 * The token's claim names (`userId`, `tenantId`, `role`) match what
 * practice-core's AuthController signs and its AuthGuard verifies
 * (src/modules/auth/*). When the issuer changes to LTI, only the
 * *source* of the token changes; this decode stays the same.
 */
import { useEffect, useState } from 'react';
import { getAuthToken } from './auth-token';

export interface Session {
  userId: string;
  tenantId: string;
  role: string;
}

function decodeClaims(token: string): Session | null {
  try {
    const payload = token.split('.')[1];
    const json = JSON.parse(
      atob(payload.replace(/-/g, '+').replace(/_/g, '/')),
    );
    if (typeof json.userId !== 'string' || typeof json.tenantId !== 'string') {
      return null;
    }
    return {
      userId: json.userId,
      tenantId: json.tenantId,
      role: typeof json.role === 'string' ? json.role : 'learner',
    };
  } catch {
    return null;
  }
}

/**
 * Resolves the current session: ensures a valid token exists (minting
 * one via the dev-login flow if needed, same as any API call would),
 * then decodes identity from it. `null` while loading.
 */
export function useSession(): Session | null {
  const [session, setSession] = useState<Session | null>(null);

  useEffect(() => {
    let cancelled = false;
    getAuthToken()
      .then((token) => {
        if (!cancelled) setSession(decodeClaims(token));
      })
      .catch(() => {
        if (!cancelled) setSession(null);
      });
    return () => {
      cancelled = true;
    };
  }, []);

  return session;
}
