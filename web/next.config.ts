import type { NextConfig } from "next";

/**
 * Phase 3 standardization: previously the untouched Next.js scaffold
 * default (no headers() at all). A full Content-Security-Policy is
 * deliberately NOT added here yet -- this app embeds Monaco
 * (WorkspaceEditor.tsx) and xterm.js (WorkspaceTerminal.tsx), both of
 * which spin up their own web workers and, in Monaco's case, sometimes
 * needs eval-like code paths for language services; a CSP tight enough
 * to matter security-wise but wrong for either of those would silently
 * break the workspace editor or terminal in a way that's easy to miss
 * without deep manual testing across every editor/terminal interaction.
 * These four headers are the ones safe to apply universally with no
 * risk of breaking the embedded editor/terminal.
 */
const nextConfig: NextConfig = {
  // Emit a self-contained server bundle (.next/standalone) so the Docker
  // runtime stage ships just that + static assets, no full node_modules.
  // PHASE1_MVP_COMPLETION.md §1.1.
  output: 'standalone',
  async headers() {
    return [
      {
        source: '/:path*',
        headers: [
          { key: 'X-Content-Type-Options', value: 'nosniff' },
          { key: 'X-Frame-Options', value: 'SAMEORIGIN' },
          { key: 'Referrer-Policy', value: 'strict-origin-when-cross-origin' },
          { key: 'Permissions-Policy', value: 'camera=(), microphone=(), geolocation=()' },
        ],
      },
    ];
  },
};

export default nextConfig;
