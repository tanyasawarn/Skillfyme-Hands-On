// @ts-check
//
// Boundary-only ESLint config -- CI-blocking (`npm run lint:boundaries`).
//
// The broad `npm run lint` is intentionally report-only right now
// (`|| true` in ci.yml) because the codebase carries ~68 pre-existing
// unrelated `no-unsafe-*` findings on `any`-typed gRPC handling. That
// backlog must not be able to mask a genuine module-boundary
// violation, so the boundary rule runs on its own, with nothing else
// enabled, and hard-fails.
//
// Rule: code outside `src/modules/evaluation/` may import the Evaluation
// module ONLY through its public seam -- the five files listed in
// SEAM below, which are exactly what `EvaluationModule` exports. A deep
// import of any other evaluation/ file is an error.
//
// Implemented with a single `no-restricted-imports` regex (the `group`
// form does NOT support gitignore-style `!` negation -- a negated entry
// there silently disables the whole rule, verified). The regex matches
// any specifier ending in `.../evaluation/<something>` and uses a
// negative lookahead to exempt the seam basenames.
//
// Widening the seam is deliberate: add the basename to SEAM here AND to
// `EvaluationModule.exports` in the same change.
import tseslint from 'typescript-eslint';
import globals from 'globals';

const SEAM = [
  'evaluation.module',
  'evaluation.service',
  'artifact.service',
  'validator-runner.service',
  'validator-executor.interface',
];

// (^|/)                     -- start of specifier, or a path separator
// (\.\./)*                  -- any number of leading ../
// (modules/)?               -- optional "modules/" segment
// evaluation/               -- the module dir
// (?!<seam>)                -- NOT immediately followed by a seam basename
const BOUNDARY_REGEX =
  '(^|/)(\\.\\./)*(modules/)?evaluation/(?!' +
  SEAM.map((s) => s.replace(/\./g, '\\.')).join('|') +
  ')';

export default tseslint.config(
  {
    ignores: [
      'eslint.config.mjs',
      'eslint.boundaries.mjs',
      'dist/**',
      'node_modules/**',
      // Test files (unit + integration) legitimately assemble the DI
      // graph from concrete providers to build a NestJS TestingModule --
      // that is test-harness wiring, not a sibling module reaching past
      // the seam. What must stay clean is production code under src/.
      'test/**',
    ],
  },
  {
    files: ['src/**/*.ts'],
    ignores: ['src/modules/evaluation/**'],
    languageOptions: {
      globals: { ...globals.node, ...globals.jest },
      sourceType: 'commonjs',
      parser: tseslint.parser,
    },
    rules: {
      'no-restricted-imports': [
        'error',
        {
          patterns: [
            {
              regex: BOUNDARY_REGEX,
              message:
                'Import the Evaluation module only through its public seam ' +
                '(evaluation.module, evaluation.service, artifact.service, ' +
                'validator-runner.service, validator-executor.interface). ' +
                'Deep imports into evaluation/ internals are forbidden -- see ' +
                'evaluation/README.md. To widen the seam, add the basename to ' +
                'SEAM in eslint.boundaries.mjs AND to EvaluationModule exports ' +
                'in the same PR.',
            },
          ],
        },
      ],
    },
  },
);
