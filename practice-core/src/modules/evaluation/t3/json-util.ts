/**
 * Phase 3 (1.8). Small typed helpers for chewing through the loosely
 * shaped JSON that `tfsec` / `checkov` / `trivy` / `aws` emit. Keeps the
 * executors free of `any` / unsafe-member-access lint errors and forces
 * an explicit "this might not be there" at every access.
 */

export type JsonValue =
  null | boolean | number | string | JsonValue[] | { [k: string]: JsonValue };

/** Parse JSON, tolerating a leading non-JSON log line some CLIs print. */
export function parseLooseJson(s: string): JsonValue | null {
  try {
    const i = s.search(/[[{]/);
    if (i < 0) return null;
    return JSON.parse(s.slice(i)) as JsonValue;
  } catch {
    return null;
  }
}

export function asRecord(v: JsonValue | undefined): Record<string, JsonValue> {
  return v && typeof v === 'object' && !Array.isArray(v) ? v : {};
}

export function asArray(v: JsonValue | undefined): JsonValue[] {
  return Array.isArray(v) ? v : [];
}

export function asString(v: JsonValue | undefined, fallback = ''): string {
  return typeof v === 'string' ? v : fallback;
}

export function asNumber(v: JsonValue | undefined, fallback = 0): number {
  return typeof v === 'number' ? v : fallback;
}

export function asBool(v: JsonValue | undefined, fallback = false): boolean {
  return typeof v === 'boolean' ? v : fallback;
}

/** First defined string among the given keys of a record. */
export function pickString(
  rec: Record<string, JsonValue>,
  keys: string[],
  fallback = '',
): string {
  for (const k of keys) {
    const val = rec[k];
    if (typeof val === 'string' && val.length > 0) return val;
  }
  return fallback;
}
