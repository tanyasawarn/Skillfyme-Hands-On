import { execFileSync } from 'node:child_process';
import * as path from 'node:path';
import { LocalShellRunner } from '../../src/modules/evaluation/t3/local-shell-runner';
import { runIacState } from '../../src/modules/evaluation/t3/iac-state.executor';
import { runStaticAnalysis } from '../../src/modules/evaluation/t3/static-analysis.executor';
import { runCloudAssert } from '../../src/modules/evaluation/t3/cloud-assert.executor';

/**
 * Phase 3 (PLAN_PHASE3_PROJECTS.md 1.8 / B3). The three early T3 validator
 * executors, run through LocalShellRunner against the static Terraform
 * fixture repo in test/fixtures/t3-terraform/ — the de-risking path the
 * plan calls out ("built and tested against a static Terraform repo ...
 * before the full driver exists").
 *
 * Tests that need a binary not on PATH (terraform/tofu, tfsec, aws) skip
 * rather than fail — CI without those tools still runs the unit suite.
 */

const FIXTURES = path.resolve(__dirname, '../fixtures/t3-terraform');

function have(bin: string): boolean {
  try {
    execFileSync(bin, ['--version'], { stdio: 'ignore' });
    return true;
  } catch {
    return false;
  }
}
const HAVE_TF = have('terraform') || have('tofu');
const TF_BIN = have('terraform') ? 'terraform' : 'tofu';
const HAVE_TFSEC = have('tfsec');

function tfInit(dir: string): void {
  execFileSync(TF_BIN, ['init', '-no-color', '-input=false'], {
    cwd: path.join(FIXTURES, dir),
    stdio: 'ignore',
  });
}

describe('T3 validator executors (integration, LocalShellRunner + static fixtures) — Phase 3 1.8', () => {
  const runner = new LocalShellRunner(FIXTURES);

  describe('IAC_STATE', () => {
    const maybe = HAVE_TF ? it : it.skip;

    beforeAll(() => {
      if (HAVE_TF) {
        tfInit('clean');
        tfInit('drifted');
        tfInit('with-secrets');
      }
    }, 120_000);

    maybe(
      'PASS: clean fixture — no drift, and secrets check finds nothing',
      async () => {
        const res = await runIacState(runner, 'v.iac', {
          working_dir: 'clean',
          no_drift: true,
          forbid_secrets_in_state: true,
        });
        expect(res.status).toBe('PASS');
        const observed = res.observed as {
          checks: Array<{ name: string; pass: boolean }>;
        };
        expect(observed.checks.find((c) => c.name === 'no_drift')?.pass).toBe(
          true,
        );
        expect(
          observed.checks.find((c) => c.name === 'forbid_secrets_in_state')
            ?.pass,
        ).toBe(true);
      },
      120_000,
    );

    maybe(
      'FAIL: drifted fixture — terraform plan reports pending changes',
      async () => {
        const res = await runIacState(runner, 'v.iac', {
          working_dir: 'drifted',
          no_drift: true,
        });
        expect(res.status).toBe('FAIL');
        expect(res.evidenceRef).toMatch(/no_drift/);
      },
      120_000,
    );

    maybe(
      'FAIL: with-secrets fixture — an AKIA key and a *_password value in state',
      async () => {
        const res = await runIacState(runner, 'v.iac', {
          working_dir: 'with-secrets',
          forbid_secrets_in_state: true,
        });
        expect(res.status).toBe('FAIL');
        expect(res.evidenceRef).toMatch(/secret/i);
      },
      120_000,
    );

    maybe(
      'PASS: require_remote_backend — a local tfstate file present FAILS the check',
      async () => {
        const res = await runIacState(runner, 'v.iac', {
          working_dir: 'clean',
          require_remote_backend: true,
        });
        // clean/ uses a local backend (terraform.tfstate on disk) → the
        // check must FAIL, and it must be a clean FAIL, not an ERROR.
        expect(res.status).toBe('FAIL');
        expect(res.evidenceRef).toMatch(/require_remote_backend/);
      },
      120_000,
    );

    it('ERROR (not FAIL): a working_dir with no terraform binary reachable', async () => {
      // Point at a non-existent dir; state pull can't run → ERROR.
      const res = await runIacState(runner, 'v.iac', {
        working_dir: 'does-not-exist',
        no_drift: true,
      });
      expect(res.status).toBe('ERROR');
    });

    it('ERROR: no checks enabled in config', async () => {
      const res = await runIacState(runner, 'v.iac', { working_dir: 'clean' });
      expect(res.status).toBe('ERROR');
      expect(res.evidenceRef).toMatch(/no checks/i);
    });
  });

  describe('STATIC_ANALYSIS (tfsec)', () => {
    const maybe = HAVE_TFSEC ? it : it.skip;

    maybe(
      'FAIL: insecure fixture at NONE threshold — tfsec reports HIGH/CRITICAL',
      async () => {
        const res = await runStaticAnalysis(runner, 'v.sa', {
          tool: 'tfsec',
          target: 'insecure',
          max_severity_allowed: 'NONE',
        });
        expect(res.status).toBe('FAIL');
        const observed = res.observed as {
          total_findings: number;
          over_threshold: number;
        };
        expect(observed.total_findings).toBeGreaterThan(0);
        expect(observed.over_threshold).toBeGreaterThan(0);
      },
      180_000,
    );

    maybe(
      'PASS: clean fixture — tfsec finds nothing above LOW',
      async () => {
        const res = await runStaticAnalysis(runner, 'v.sa', {
          tool: 'tfsec',
          target: 'clean',
          max_severity_allowed: 'LOW',
        });
        expect(res.status).toBe('PASS');
      },
      180_000,
    );

    maybe(
      'max_findings: insecure fixture passes if the budget is high enough',
      async () => {
        const res = await runStaticAnalysis(runner, 'v.sa', {
          tool: 'tfsec',
          target: 'insecure',
          max_severity_allowed: 'MEDIUM',
          max_findings: 999,
        });
        expect(res.status).toBe('PASS');
      },
      180_000,
    );

    it('ERROR: no tool configured', async () => {
      const res = await runStaticAnalysis(runner, 'v.sa', { target: 'clean' });
      expect(res.status).toBe('ERROR');
    });

    it('ERROR (not FAIL): tool binary missing', async () => {
      const res = await runStaticAnalysis(runner, 'v.sa', {
        tool: 'checkov', // not installed in this environment
        target: 'clean',
      });
      // checkov absent → LocalShellRunner returns infraError → ERROR
      expect(res.status).toBe('ERROR');
    });
  });

  describe('CLOUD_ASSERT', () => {
    it('ERROR: unknown check id', async () => {
      const res = await runCloudAssert(runner, 'v.ca', {
        checks: ['no_such_check'],
      });
      expect(res.status).toBe('ERROR');
      expect(res.evidenceRef).toMatch(/unknown check/i);
    });

    it('ERROR: no checks configured', async () => {
      const res = await runCloudAssert(runner, 'v.ca', { checks: [] });
      expect(res.status).toBe('ERROR');
    });

    it('ERROR (not FAIL): aws CLI missing or unauthenticated', async () => {
      // With placeholder/no creds, `aws s3api list-buckets` fails →
      // the check can't run → whole validator ERROR, never scored.
      const res = await runCloudAssert(runner, 'v.ca', {
        checks: ['no_public_s3'],
        params: { region: 'us-east-1' },
      });
      expect(res.status).toBe('ERROR');
    });
  });
});
