import type { ValidatorExecutionResult } from '../validator-executor.interface';
import type { ShellRunner } from './shell-runner';
import { asNumber, asRecord, asString, parseLooseJson } from './json-util';

/**
 * Phase 3 (PLAN_PHASE3_PROJECTS.md 3.7 / B3). TEST_SUITE validator: runs
 * the repo's own test command inside the T3 workspace and parses the
 * report (JUnit XML / TAP / jest-json / go-json) for a pass rate.
 *
 * Config (mirrors ActivitySpecValidatorTestSuiteConfig):
 *   command:        the test command, run via the shell (required)
 *   report_format?: 'junit' | 'tap' | 'jest-json' | 'go-json' (default: infer)
 *   report_path?:   where the command writes its report (for junit/jest-json)
 *   min_pass_rate?: floor on passed/total (default 1.0 — all must pass)
 *
 * A command that fails to run at all → ERROR (never scored). A command
 * that runs and reports failures → FAIL with the counts.
 */
export interface TestSuiteConfig {
  command: string;
  report_format?: 'junit' | 'tap' | 'jest-json' | 'go-json';
  report_path?: string;
  min_pass_rate?: number;
}

interface Counts {
  total: number;
  passed: number;
  failed: number;
  skipped: number;
}

export async function runTestSuite(
  runner: ShellRunner,
  validatorId: string,
  rawConfig: unknown,
): Promise<ValidatorExecutionResult> {
  const start = Date.now();
  const cfg = (rawConfig ?? {}) as TestSuiteConfig;
  const err = (detail: string): ValidatorExecutionResult => ({
    validatorId,
    status: 'ERROR',
    durationMs: Date.now() - start,
    evidenceRef: detail,
  });

  if (!cfg.command)
    return err('TEST_SUITE validator has no `command` configured');
  const minPassRate = cfg.min_pass_rate ?? 1.0;

  const run = await runner.run({
    argv: ['sh', '-c', cfg.command],
    timeoutMs: 600_000,
  });
  if (run.infraError)
    return err(`test command could not run: ${run.infraError}`);

  // Get the report text: from report_path if given, else stdout.
  let reportText = run.stdout;
  if (cfg.report_path) {
    const cat = await runner.run({
      argv: ['cat', cfg.report_path],
      timeoutMs: 30_000,
    });
    if (cat.infraError || cat.exitCode !== 0) {
      return err(
        `test ran (exit ${run.exitCode}) but the report at ${cfg.report_path} could not be read`,
      );
    }
    reportText = cat.stdout;
  }

  const format =
    cfg.report_format ?? inferFormat(reportText, cfg.report_path ?? '');
  let counts: Counts | null;
  try {
    counts = parseReport(format, reportText);
  } catch (e) {
    return err(
      `could not parse the ${format} test report: ${e instanceof Error ? e.message : String(e)}`,
    );
  }
  if (!counts || counts.total === 0) {
    // No parseable report — fall back to the exit code alone.
    const passed = run.exitCode === 0;
    return {
      validatorId,
      status: passed ? 'PASS' : 'FAIL',
      observed: {
        backend: runner.backend,
        parsed: false,
        exit_code: run.exitCode,
        note: 'no parseable test report — used the command exit code',
      },
      expected: { min_pass_rate: minPassRate },
      durationMs: Date.now() - start,
      evidenceRef: passed ? undefined : `test command exited ${run.exitCode}`,
    };
  }

  const passRate =
    counts.total === 0
      ? 1
      : counts.passed / (counts.total - counts.skipped || 1);
  const ok = passRate >= minPassRate;
  return {
    validatorId,
    status: ok ? 'PASS' : 'FAIL',
    observed: {
      backend: runner.backend,
      format,
      ...counts,
      pass_rate: Number(passRate.toFixed(4)),
    },
    expected: { min_pass_rate: minPassRate },
    durationMs: Date.now() - start,
    evidenceRef: ok
      ? undefined
      : `pass rate ${(passRate * 100).toFixed(1)}% below ${(minPassRate * 100).toFixed(0)}% (${counts.failed} failed / ${counts.total} total)`,
  };
}

function inferFormat(
  text: string,
  path: string,
): TestSuiteConfig['report_format'] {
  if (path.endsWith('.xml') || /<testsuite/i.test(text)) return 'junit';
  if (/^\s*TAP version \d+/m.test(text) || /^ok \d+/m.test(text)) return 'tap';
  if (/"numPassedTests"/.test(text)) return 'jest-json';
  if (/"Action":"(pass|fail|run)"/.test(text)) return 'go-json';
  return 'junit';
}

function parseReport(
  format: TestSuiteConfig['report_format'],
  text: string,
): Counts | null {
  switch (format) {
    case 'junit':
      return parseJUnit(text);
    case 'tap':
      return parseTAP(text);
    case 'jest-json':
      return parseJestJSON(text);
    case 'go-json':
      return parseGoJSON(text);
    default:
      return null;
  }
}

function parseJUnit(xml: string): Counts | null {
  // Sum the attributes across all <testsuite ...> elements.
  const suites = xml.match(/<testsuite\b[^>]*>/gi) ?? [];
  if (suites.length === 0) return null;
  let total = 0;
  let failed = 0;
  let skipped = 0;
  for (const s of suites) {
    total += attr(s, 'tests');
    failed += attr(s, 'failures') + attr(s, 'errors');
    skipped += attr(s, 'skipped');
  }
  return { total, passed: total - failed - skipped, failed, skipped };
}

function attr(tag: string, name: string): number {
  const m = new RegExp(`${name}="(\\d+)"`).exec(tag);
  return m ? parseInt(m[1], 10) : 0;
}

function parseTAP(text: string): Counts | null {
  const lines = text.split('\n');
  let passed = 0;
  let failed = 0;
  let skipped = 0;
  for (const line of lines) {
    const m = /^(ok|not ok)\s+\d+/.exec(line.trim());
    if (!m) continue;
    if (/# SKIP/i.test(line)) skipped++;
    else if (m[1] === 'ok') passed++;
    else failed++;
  }
  const total = passed + failed + skipped;
  return total === 0 ? null : { total, passed, failed, skipped };
}

function parseJestJSON(text: string): Counts | null {
  const j = asRecord(parseLooseJson(text) ?? undefined);
  if (j.numTotalTests === undefined) return null;
  const total = asNumber(j.numTotalTests);
  const passed = asNumber(j.numPassedTests);
  const failed = asNumber(j.numFailedTests);
  const skipped = asNumber(j.numPendingTests) + asNumber(j.numTodoTests);
  return { total, passed, failed, skipped };
}

function parseGoJSON(text: string): Counts | null {
  // `go test -json` emits one JSON object per line; count the terminal
  // Action per test.
  let passed = 0;
  let failed = 0;
  let skipped = 0;
  for (const line of text.split('\n')) {
    const t = line.trim();
    if (!t.startsWith('{')) continue;
    const o = asRecord(parseLooseJson(t) ?? undefined);
    if (!asString(o.Test)) continue; // package-level lines have no Test
    const action = asString(o.Action);
    if (action === 'pass') passed++;
    else if (action === 'fail') failed++;
    else if (action === 'skip') skipped++;
  }
  const total = passed + failed + skipped;
  return total === 0 ? null : { total, passed, failed, skipped };
}
