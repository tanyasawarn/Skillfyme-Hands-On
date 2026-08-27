/**
 * Frontend half of the dev-mode auth flow (practice-core's
 * POST /v1/auth/dev-login, src/modules/auth/auth.controller.ts). The
 * backend's AuthGuard now rejects every request without a valid bearer
 * JWT -- this module is what makes the frontend able to call the API at
 * all again. Dev-only by design (no login form, no password): fetches a
 * token for the same demo user/tenant every API call site already uses
 * (lib/demo-context.ts), caches it in memory + localStorage so a page
 * reload doesn't re-mint a token on every request, and re-fetches once
 * the cached one is within 5 minutes of its stated expiry.
 */
import { API_BASE_URL } from './config';

const STORAGE_KEY = 'pe_dev_token';
const REFRESH_MARGIN_MS = 5 * 60 * 1000;

interface StoredToken {
  token: string;
  expiresAt: number; // epoch ms
}

let inFlight: Promise<string> | null = null;

function readStoredToken(): StoredToken | null {
  if (typeof window === 'undefined') return null;
  const raw = window.localStorage.getItem(STORAGE_KEY);
  if (!raw) return null;
  try {
    return JSON.parse(raw) as StoredToken;
  } catch {
    return null;
  }
}

function writeStoredToken(entry: StoredToken) {
  if (typeof window === 'undefined') return;
  window.localStorage.setItem(STORAGE_KEY, JSON.stringify(entry));
}

async function mintToken(): Promise<StoredToken> {
  const res = await fetch(`${API_BASE_URL}/v1/auth/dev-login`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({}),
  });
  if (!res.ok) {
    throw new Error(`dev-login failed: ${res.status}`);
  }
  const body = (await res.json()) as { token: string; expiresIn: string };
  // expiresIn is a jsonwebtoken-style duration string ("12h") on the
  // response but the token's own `exp` claim is the authoritative
  // expiry -- decode it rather than re-parsing the duration string, so
  // this stays correct if the backend's TTL ever changes.
  const expiresAt = decodeExpiry(body.token) ?? Date.now() + 11 * 60 * 60 * 1000;
  return { token: body.token, expiresAt };
}

function decodeExpiry(token: string): number | null {
  try {
    const payload = token.split('.')[1];
    const json = JSON.parse(atob(payload.replace(/-/g, '+').replace(/_/g, '/')));
    return typeof json.exp === 'number' ? json.exp * 1000 : null;
  } catch {
    return null;
  }
}

/** Returns a valid bearer token, minting a fresh one if none is cached or the cached one is near expiry. */
export async function getAuthToken(): Promise<string> {
  const stored = readStoredToken();
  if (stored && stored.expiresAt - Date.now() > REFRESH_MARGIN_MS) {
    return stored.token;
  }

  if (!inFlight) {
    inFlight = mintToken()
      .then((entry) => {
        writeStoredToken(entry);
        return entry.token;
      })
      .finally(() => {
        inFlight = null;
      });
  }
  return inFlight;
}
