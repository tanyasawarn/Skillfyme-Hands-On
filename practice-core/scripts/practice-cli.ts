#!/usr/bin/env node
/**
 * practice-cli: PLAN.md M1.2's "YAML spec -> JSONB projection;
 * practice-cli validate/test/publish" -- previously non-existent as a
 * unified tool; content tooling was 15 independent scripts invoked
 * directly via `npx ts-node scripts/<name>.ts`, each real and working,
 * but with no single `practice-cli <subcommand>` entry point matching
 * what the milestone (and doc §3.6's authoring-workflow framing) names.
 *
 * A thin dispatcher over the three scripts that already implement each
 * subcommand's real logic (lint-content.ts, content-ci.ts,
 * publish-all-content.ts) -- deliberately NOT a rewrite of their logic
 * into this file. Each of those scripts calls process.exit() directly
 * (a real, working pattern for a standalone script), which makes them
 * unsafe to `import` and call in-process (their exit call would kill
 * this CLI's own process before it could report anything), so this
 * dispatches via spawnSync and propagates the child's real exit code --
 * `practice-cli validate`'s exit code IS lint-content.ts's exit code,
 * not a reinterpretation of it.
 *
 * Usage:
 *   practice-cli validate              # lint every activity YAML (no DB writes beyond skill-slug lookups)
 *   practice-cli test [activity-id]    # golden-path/null-path/flake harness against a real orchestrator
 *   practice-cli publish               # lint + publish every activity YAML
 *
 * Installed as a real bin entry (package.json "bin") -- `npm link` or a
 * global install makes `practice-cli` resolve on PATH; run directly via
 * `npx ts-node scripts/practice-cli.ts <subcommand>` without either.
 */
import { spawnSync } from 'node:child_process';
import * as path from 'node:path';

const SUBCOMMANDS: Record<string, string> = {
  validate: 'lint-content.ts',
  test: 'content-ci.ts',
  publish: 'publish-all-content.ts',
};

function usage(): never {
  console.error('usage: practice-cli <validate|test|publish> [args...]');
  console.error('');
  console.error('  validate            lint every activity YAML against the schema + skill/DAG checks');
  console.error('  test [activity-id]  golden-path/null-path/flake harness (needs a real orchestrator + k3s)');
  console.error('  publish             lint and publish every activity YAML');
  process.exit(2);
}

function main() {
  const [subcommand, ...rest] = process.argv.slice(2);
  if (!subcommand || !(subcommand in SUBCOMMANDS)) {
    usage();
  }

  const scriptPath = path.resolve(__dirname, SUBCOMMANDS[subcommand]);
  // ts-node -r tsconfig-paths/register, matching exactly how every one
  // of these scripts' own doc comments says to invoke them manually --
  // this CLI is that same invocation, just under one name instead of
  // fifteen.
  const result = spawnSync(
    process.execPath,
    ['-r', 'ts-node/register', '-r', 'tsconfig-paths/register', scriptPath, ...rest],
    { stdio: 'inherit', cwd: path.resolve(__dirname, '..') },
  );

  if (result.error) {
    console.error(`practice-cli: failed to run ${subcommand}: ${result.error.message}`);
    process.exit(1);
  }
  process.exit(result.status ?? 1);
}

main();
