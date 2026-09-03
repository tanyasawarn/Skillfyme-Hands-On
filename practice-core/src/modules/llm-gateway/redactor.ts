/**
 * Doc §7.6 Redaction: "Strip credentials, tokens, learner PII from
 * context before egress. Scan environment state summaries for secret
 * patterns."
 *
 * Pure function -- runs on the fully-rendered system + user text just
 * before it leaves the platform for a provider.
 */

const PATTERNS: Array<{ re: RegExp; label: string }> = [
  // AWS
  { re: /\bAKIA[0-9A-Z]{16}\b/g, label: 'aws_access_key_id' },
  {
    re: /\baws_secret_access_key\s*[=:]\s*[A-Za-z0-9/+]{40}\b/gi,
    label: 'aws_secret_access_key',
  },
  // Generic bearer / api keys
  { re: /\bsk-[A-Za-z0-9_-]{16,}\b/g, label: 'api_key' },
  { re: /\bghp_[A-Za-z0-9]{36}\b/g, label: 'github_pat' },
  {
    re: /\b(?:authorization|x-api-key|api[_-]?key|token|secret|password)\s*[=:]\s*["']?[^\s"']{8,}["']?/gi,
    label: 'credential_kv',
  },
  // JWT
  {
    re: /\beyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\b/g,
    label: 'jwt',
  },
  // Private key blocks
  {
    re: /-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?-----END [A-Z ]*PRIVATE KEY-----/g,
    label: 'private_key',
  },
  // Email (learner PII)
  {
    re: /\b[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}\b/g,
    label: 'email',
  },
];

export interface RedactionResult {
  text: string;
  hits: string[];
}

export function redact(text: string): RedactionResult {
  let out = text;
  const hits: string[] = [];
  for (const { re, label } of PATTERNS) {
    out = out.replace(re, () => {
      hits.push(label);
      return `[redacted:${label}]`;
    });
  }
  return { text: out, hits: Array.from(new Set(hits)) };
}
