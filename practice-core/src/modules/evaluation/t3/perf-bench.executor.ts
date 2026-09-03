import type { ValidatorExecutionResult } from '../validator-executor.interface';
import type { ShellRunner } from './shell-runner';
import { asNumber, asRecord, parseLooseJson } from './json-util';

/**
 * Phase 3 (PLAN_PHASE3_PROJECTS.md 3.7 / B3). PERF_BENCH validator: drives
 * load (k6) against the learner's deployed system and gates on p95
 * latency + error rate.
 *
 * Config (mirrors ActivitySpecValidatorPerfBenchConfig):
 *   script?:       path to a k6 script in the repo/workspace
 *   target_url?:   URL to hit (used with a generated one-liner if no script)
 *   rps?:          target requests/second (default 20)
 *   duration_s?:   test duration (default 30)
 *   p95_ms_max?:   p95 latency ceiling in ms (required to gate on latency)
 *   error_rate_max?: max error rate 0..1 (default 0.01)
 *
 * A k6 run that can't start → ERROR. A run that completes but misses the
 * thresholds → FAIL with the measured numbers.
 */
export interface PerfBenchConfig {
  script?: string;
  target_url?: string;
  rps?: number;
  duration_s?: number;
  p95_ms_max?: number;
  error_rate_max?: number;
}

export async function runPerfBench(
  runner: ShellRunner,
  validatorId: string,
  rawConfig: unknown,
): Promise<ValidatorExecutionResult> {
  const start = Date.now();
  const cfg = (rawConfig ?? {}) as PerfBenchConfig;
  const err = (detail: string): ValidatorExecutionResult => ({
    validatorId,
    status: 'ERROR',
    durationMs: Date.now() - start,
    evidenceRef: detail,
  });

  if (!cfg.script && !cfg.target_url) {
    return err('PERF_BENCH needs either `script` or `target_url`');
  }
  const rps = cfg.rps ?? 20;
  const durationS = cfg.duration_s ?? 30;
  const errorRateMax = cfg.error_rate_max ?? 0.01;
  const summaryPath = '/tmp/k6-summary.json';

  let argv: string[];
  if (cfg.script) {
    argv = [
      'k6',
      'run',
      '--quiet',
      '--summary-export',
      summaryPath,
      cfg.script,
    ];
  } else {
    // generate a minimal constant-arrival-rate script targeting the URL
    const gen = `
import http from 'k6/http';
export const options = {
  scenarios: { load: { executor: 'constant-arrival-rate', rate: ${rps}, timeUnit: '1s',
    duration: '${durationS}s', preAllocatedVUs: ${Math.max(rps, 10)}, maxVUs: ${rps * 4} } },
};
export default function () { http.get(${JSON.stringify(cfg.target_url)}); }
`.trim();
    const write = await runner.run({
      argv: ['sh', '-c', `cat > /tmp/perf-bench.js <<'EOF'\n${gen}\nEOF`],
      timeoutMs: 15_000,
    });
    if (write.infraError || write.exitCode !== 0) {
      return err('could not write the generated k6 script');
    }
    argv = [
      'k6',
      'run',
      '--quiet',
      '--summary-export',
      summaryPath,
      '/tmp/perf-bench.js',
    ];
  }

  const run = await runner.run({
    argv,
    timeoutMs: (durationS + 60) * 1000,
  });
  if (run.infraError) return err(`k6 could not run: ${run.infraError}`);

  const cat = await runner.run({
    argv: ['cat', summaryPath],
    timeoutMs: 15_000,
  });
  if (cat.infraError || cat.exitCode !== 0) {
    return err('k6 ran but its summary export could not be read');
  }
  const summary = asRecord(parseLooseJson(cat.stdout) ?? undefined);
  const metrics = asRecord(summary.metrics);
  const httpReqDuration = asRecord(metrics.http_req_duration);
  const values = asRecord(httpReqDuration.values);
  const p95 = asNumber(values['p(95)'], NaN);

  const httpReqFailed = asRecord(metrics.http_req_failed);
  const failedValues = asRecord(httpReqFailed.values);
  const errorRate = asNumber(failedValues.rate, NaN);

  const checks: Array<{ name: string; pass: boolean; detail: string }> = [];
  if (typeof cfg.p95_ms_max === 'number') {
    const pass = Number.isFinite(p95) && p95 <= cfg.p95_ms_max;
    checks.push({
      name: 'p95_latency',
      pass,
      detail: `p95 ${Number.isFinite(p95) ? p95.toFixed(1) + 'ms' : 'n/a'} vs max ${cfg.p95_ms_max}ms`,
    });
  }
  {
    const pass = !Number.isFinite(errorRate) || errorRate <= errorRateMax;
    checks.push({
      name: 'error_rate',
      pass,
      detail: `error rate ${Number.isFinite(errorRate) ? (errorRate * 100).toFixed(2) + '%' : 'n/a'} vs max ${(errorRateMax * 100).toFixed(2)}%`,
    });
  }

  if (checks.length === 0) {
    return err(
      'PERF_BENCH has no threshold configured (set p95_ms_max and/or error_rate_max)',
    );
  }
  const allPass = checks.every((c) => c.pass);
  return {
    validatorId,
    status: allPass ? 'PASS' : 'FAIL',
    observed: {
      backend: runner.backend,
      p95_ms: Number.isFinite(p95) ? Number(p95.toFixed(1)) : null,
      error_rate: Number.isFinite(errorRate)
        ? Number(errorRate.toFixed(4))
        : null,
      rps,
      duration_s: durationS,
      checks,
    },
    expected: {
      ...(cfg.p95_ms_max !== undefined ? { p95_ms_max: cfg.p95_ms_max } : {}),
      error_rate_max: errorRateMax,
    },
    durationMs: Date.now() - start,
    evidenceRef: allPass
      ? undefined
      : checks
          .filter((c) => !c.pass)
          .map((c) => c.detail)
          .join('; '),
  };
}
