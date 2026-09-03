import { runPerfBench } from './perf-bench.executor';
import { runChaosProbe } from './chaos-probe.executor';
import type { ShellRunner, ShellCommand, ShellResult } from './shell-runner';

function runnerFrom(
  handler: (cmd: ShellCommand) => Partial<ShellResult>,
): ShellRunner {
  return {
    backend: 'test',
    run: (cmd: ShellCommand): Promise<ShellResult> =>
      Promise.resolve({
        exitCode: 0,
        stdout: '',
        stderr: '',
        durationMs: 1,
        ...handler(cmd),
      }),
  };
}

describe('runPerfBench (Phase 3 3.7)', () => {
  it('ERROR when neither script nor target_url given', async () => {
    const r = await runPerfBench(
      runnerFrom(() => ({})),
      'v',
      {},
    );
    expect(r.status).toBe('ERROR');
  });

  it('ERROR (not FAIL) when k6 cannot run', async () => {
    const r = await runPerfBench(
      runnerFrom((c) =>
        c.argv[0] === 'k6' ? { infraError: 'k6 missing' } : {},
      ),
      'v',
      { target_url: 'http://svc/health', p95_ms_max: 200 },
    );
    expect(r.status).toBe('ERROR');
  });

  it('PASSes when p95 and error rate are under the thresholds', async () => {
    const summary = JSON.stringify({
      metrics: {
        http_req_duration: { values: { 'p(95)': 150.2 } },
        http_req_failed: { values: { rate: 0.002 } },
      },
    });
    const r = await runPerfBench(
      runnerFrom((c) => {
        if (c.argv[0] === 'cat') return { stdout: summary };
        return { exitCode: 0 };
      }),
      'v',
      {
        target_url: 'http://svc/health',
        p95_ms_max: 200,
        error_rate_max: 0.01,
      },
    );
    expect(r.status).toBe('PASS');
    expect(r.observed).toMatchObject({ p95_ms: 150.2 });
  });

  it('FAILs when p95 exceeds the ceiling', async () => {
    const summary = JSON.stringify({
      metrics: {
        http_req_duration: { values: { 'p(95)': 480 } },
        http_req_failed: { values: { rate: 0 } },
      },
    });
    const r = await runPerfBench(
      runnerFrom((c) =>
        c.argv[0] === 'cat' ? { stdout: summary } : { exitCode: 0 },
      ),
      'v',
      { target_url: 'http://svc/health', p95_ms_max: 200 },
    );
    expect(r.status).toBe('FAIL');
    expect(r.evidenceRef).toMatch(/p95 480/);
  });
});

describe('runChaosProbe (Phase 3 3.7)', () => {
  const healthCheck = { url: 'http://svc/health', expect_status: 200 };

  it('ERROR when no action', async () => {
    const r = await runChaosProbe(
      runnerFrom(() => ({})),
      'v',
      { health_check: healthCheck },
    );
    expect(r.status).toBe('ERROR');
  });

  it('ERROR when the service is not healthy before the chaos action', async () => {
    const r = await runChaosProbe(
      runnerFrom((c) => (c.argv[0] === 'curl' ? { stdout: '503' } : {})),
      'v',
      {
        action: 'kill_pod',
        health_check: healthCheck,
        recovery_timeout_ms: 10,
      },
    );
    expect(r.status).toBe('ERROR');
    expect(r.evidenceRef).toMatch(/not healthy before/);
  });

  it('PASSes when the service stays green through a pod kill', async () => {
    const r = await runChaosProbe(
      runnerFrom((c) => {
        if (c.argv[0] === 'curl') return { stdout: '200' };
        if (c.argv[0] === 'sh')
          return { exitCode: 0, stdout: 'pod "x" deleted' };
        return {};
      }),
      'v',
      {
        action: 'kill_pod',
        target_selector: 'app=checkout',
        health_check: { ...healthCheck, interval_ms: 1 },
        recovery_timeout_ms: 5,
      },
    );
    expect(r.status).toBe('PASS');
    expect(r.observed).toMatchObject({ action: 'kill_pod', recovered: true });
  });

  it('FAILs when the service does not recover', async () => {
    let probes = 0;
    const r = await runChaosProbe(
      runnerFrom((c) => {
        if (c.argv[0] === 'curl') {
          probes++;
          // green pre-check, then everything after the kill is 503
          return { stdout: probes === 1 ? '200' : '503' };
        }
        if (c.argv[0] === 'sh') return { exitCode: 0 };
        return {};
      }),
      'v',
      {
        action: 'kill_pod',
        health_check: { ...healthCheck, interval_ms: 1 },
        recovery_timeout_ms: 6,
      },
    );
    expect(r.status).toBe('FAIL');
    expect(r.evidenceRef).toMatch(/did not recover/);
  });

  it('ERROR when the chaos action itself cannot run', async () => {
    const r = await runChaosProbe(
      runnerFrom((c) => {
        if (c.argv[0] === 'curl') return { stdout: '200' };
        if (c.argv[0] === 'sh') return { infraError: 'kubectl missing' };
        return {};
      }),
      'v',
      {
        action: 'drain_node',
        health_check: healthCheck,
        recovery_timeout_ms: 5,
      },
    );
    expect(r.status).toBe('ERROR');
  });
});
