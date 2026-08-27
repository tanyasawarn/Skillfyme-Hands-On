import { defineConfig } from 'vitest/config';
import react from '@vitejs/plugin-react';
import path from 'node:path';

const dirname = import.meta.dirname;

/**
 * Phase 3 standardization: web/ previously had zero test infrastructure
 * (no Jest, no Vitest, no test files at all) -- unlike orchestrator/ (Go
 * tests) and practice-core/ (Jest), both real suites. Vitest chosen over
 * Jest since it's the standard pairing with Vite's transform pipeline
 * and needs far less config to work with Next.js 16 + Tailwind v4 +
 * React 19's ESM-heavy dependency graph (no ts-jest/babel-jest
 * transform config, no moduleNameMapper duplication of tsconfig's own
 * path aliases beyond what's below).
 */
export default defineConfig({
  plugins: [react()],
  test: {
    environment: 'jsdom',
    setupFiles: ['./vitest.setup.ts'],
    globals: true,
  },
  resolve: {
    alias: {
      '@': path.resolve(dirname, './src'),
    },
  },
});
