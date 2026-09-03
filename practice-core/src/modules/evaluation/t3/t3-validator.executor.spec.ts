import { T3ValidatorExecutor } from './t3-validator.executor';
import type { GrpcValidatorExecutor } from '../grpc-validator-executor';
import type { ShellRunner } from './shell-runner';
import type {
  ValidatorExecutionResult,
  ValidatorSpec,
} from '../validator-executor.interface';

/**
 * Phase 3 (1.8 / B3). Dispatch behaviour of T3ValidatorExecutor:
 *  - non-T3 types are delegated verbatim to GrpcValidatorExecutor
 *  - T3 types with no shell runner wired return ERROR (never scored)
 *  - T3 types route to the right typed handler via the shell runner
 * The handlers themselves are covered by the integration spec against
 * real terraform/tfsec.
 */
function spec(overrides: Partial<ValidatorSpec>): ValidatorSpec {
  return {
    id: 'v1',
    type: 'SHELL_ASSERT',
    expect: {},
    weight: 1,
    ...overrides,
  };
}

describe('T3ValidatorExecutor dispatch', () => {
  it('delegates non-T3 types straight to GrpcValidatorExecutor', async () => {
    const delegateResult: ValidatorExecutionResult = {
      validatorId: 'v1',
      status: 'PASS',
      durationMs: 1,
    };
    const execute = jest.fn().mockResolvedValue(delegateResult);
    const delegate = { execute } as unknown as GrpcValidatorExecutor;

    const exec = new T3ValidatorExecutor(delegate, undefined);
    const res = await exec.execute(
      'env-1',
      'att-1',
      spec({ type: 'K8S_ASSERT' }),
    );

    expect(res.status).toBe('PASS');
    expect(execute).toHaveBeenCalledWith(
      'env-1',
      'att-1',
      expect.objectContaining({ type: 'K8S_ASSERT' }),
    );
  });

  it('returns ERROR (not FAIL, not a throw) for a T3 type with no shell runner', async () => {
    const execute = jest.fn();
    const delegate = { execute } as unknown as GrpcValidatorExecutor;
    const exec = new T3ValidatorExecutor(delegate, undefined);

    const res = await exec.execute(
      'env-1',
      'att-1',
      spec({ type: 'IAC_STATE', config: { iac_state: { no_drift: true } } }),
    );

    expect(res.status).toBe('ERROR');
    expect(execute).not.toHaveBeenCalled();
  });

  it('routes IAC_STATE / CLOUD_ASSERT / STATIC_ANALYSIS through the shell runner', async () => {
    const calls: string[] = [];
    const fakeRunner: ShellRunner = {
      backend: 'test',
      run: (cmd) => {
        calls.push(cmd.argv[0]);
        // every command is an infra failure → handler yields ERROR,
        // which is fine: we only assert the runner was reached.
        return Promise.resolve({
          exitCode: -1,
          stdout: '',
          stderr: '',
          infraError: 'test runner: no real backend',
          durationMs: 1,
        });
      },
    };
    const execute = jest.fn();
    const delegate = { execute } as unknown as GrpcValidatorExecutor;
    const exec = new T3ValidatorExecutor(delegate, fakeRunner);

    const iac = await exec.execute(
      'e',
      'a',
      spec({ type: 'IAC_STATE', config: { iac_state: { no_drift: true } } }),
    );
    const sa = await exec.execute(
      'e',
      'a',
      spec({
        type: 'STATIC_ANALYSIS',
        config: { static_analysis: { tool: 'tfsec' } },
      }),
    );
    const ca = await exec.execute(
      'e',
      'a',
      spec({
        type: 'CLOUD_ASSERT',
        config: { cloud_assert: { checks: ['no_public_s3'] } },
      }),
    );

    expect(iac.status).toBe('ERROR'); // infra failure surfaced as ERROR
    expect(sa.status).toBe('ERROR');
    expect(ca.status).toBe('ERROR');
    expect(calls).toEqual(
      expect.arrayContaining(['terraform', 'tfsec', 'aws']),
    );
    expect(execute).not.toHaveBeenCalled();
  });

  it('a handler that throws becomes ERROR, not an unhandled rejection', async () => {
    const throwingRunner: ShellRunner = {
      backend: 'test',
      run: () => {
        throw new Error('boom');
      },
    };
    const execute = jest.fn();
    const delegate = { execute } as unknown as GrpcValidatorExecutor;
    const exec = new T3ValidatorExecutor(delegate, throwingRunner);

    const res = await exec.execute(
      'e',
      'a',
      spec({
        type: 'CLOUD_ASSERT',
        config: { cloud_assert: { checks: ['no_public_s3'] } },
      }),
    );
    expect(res.status).toBe('ERROR');
    expect(res.evidenceRef).toMatch(/boom/);
  });
});
