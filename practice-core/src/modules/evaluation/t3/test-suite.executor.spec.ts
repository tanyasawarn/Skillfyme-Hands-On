import { runTestSuite } from './test-suite.executor';
import type { ShellRunner, ShellCommand, ShellResult } from './shell-runner';

/** A ShellRunner that returns scripted results keyed by the first argv token. */
function scriptedRunner(
  responses: Record<string, Partial<ShellResult>>,
  fileContents: Record<string, string> = {},
): ShellRunner {
  return {
    backend: 'test',
    run: (cmd: ShellCommand): Promise<ShellResult> => {
      const base: ShellResult = {
        exitCode: 0,
        stdout: '',
        stderr: '',
        durationMs: 1,
      };
      if (cmd.argv[0] === 'cat') {
        const path = cmd.argv[1];
        if (fileContents[path] !== undefined) {
          return Promise.resolve({ ...base, stdout: fileContents[path] });
        }
        return Promise.resolve({
          ...base,
          exitCode: 1,
          stderr: 'no such file',
        });
      }
      const key = cmd.argv[0] === 'sh' ? 'sh' : cmd.argv[0];
      return Promise.resolve({ ...base, ...(responses[key] ?? {}) });
    },
  };
}

describe('runTestSuite (Phase 3 3.7)', () => {
  it('ERROR when no command configured', async () => {
    const r = await runTestSuite(scriptedRunner({}), 'v', {});
    expect(r.status).toBe('ERROR');
  });

  it('ERROR (not FAIL) when the command cannot run', async () => {
    const r = await runTestSuite(
      scriptedRunner({ sh: { infraError: 'sh missing' } }),
      'v',
      { command: 'npm test' },
    );
    expect(r.status).toBe('ERROR');
  });

  it('parses a JUnit report and PASSes when all green', async () => {
    const junit =
      '<testsuites><testsuite tests="10" failures="0" errors="0" skipped="1"></testsuite></testsuites>';
    const r = await runTestSuite(
      scriptedRunner({ sh: { exitCode: 0 } }, { '/rep.xml': junit }),
      'v',
      { command: 'go test', report_format: 'junit', report_path: '/rep.xml' },
    );
    expect(r.status).toBe('PASS');
    expect(r.observed).toMatchObject({ total: 10, passed: 9, skipped: 1 });
  });

  it('FAILs when the JUnit report shows failures below min_pass_rate', async () => {
    const junit =
      '<testsuite tests="10" failures="3" errors="0" skipped="0"></testsuite>';
    const r = await runTestSuite(
      scriptedRunner({ sh: { exitCode: 1 } }, { '/rep.xml': junit }),
      'v',
      { command: 'go test', report_path: '/rep.xml', min_pass_rate: 1.0 },
    );
    expect(r.status).toBe('FAIL');
    expect(r.evidenceRef).toMatch(/3 failed/);
  });

  it('parses TAP from stdout', async () => {
    const tap =
      'TAP version 13\nok 1 a\nok 2 b\nnot ok 3 c\nok 4 d # SKIP later\n1..4';
    const r = await runTestSuite(
      scriptedRunner({ sh: { exitCode: 1, stdout: tap } }),
      'v',
      {
        command: 'prove',
        min_pass_rate: 0.9,
      },
    );
    expect(r.status).toBe('FAIL');
    expect(r.observed).toMatchObject({ passed: 2, failed: 1, skipped: 1 });
  });

  it('parses jest-json from stdout', async () => {
    const jestJson = JSON.stringify({
      numTotalTests: 20,
      numPassedTests: 20,
      numFailedTests: 0,
      numPendingTests: 0,
    });
    const r = await runTestSuite(
      scriptedRunner({ sh: { exitCode: 0, stdout: jestJson } }),
      'v',
      {
        command: 'jest --json',
      },
    );
    expect(r.status).toBe('PASS');
    expect(r.observed).toMatchObject({ total: 20, passed: 20 });
  });

  it('falls back to the exit code when there is no parseable report', async () => {
    const r = await runTestSuite(
      scriptedRunner({ sh: { exitCode: 0, stdout: 'ran fine' } }),
      'v',
      {
        command: 'make test',
      },
    );
    expect(r.status).toBe('PASS');
    expect(r.observed).toMatchObject({ parsed: false, exit_code: 0 });
  });
});
