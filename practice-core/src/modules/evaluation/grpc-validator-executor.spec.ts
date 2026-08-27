import { GrpcValidatorExecutor } from './grpc-validator-executor';
import type { ValidatorSpec } from './validator-executor.interface';

/**
 * GrpcValidatorExecutor.call() dials a real gRPC client set up in
 * onModuleInit(); these tests never call onModuleInit(), so they stub
 * call() directly to exercise the NO_REGRESSION request-shaping and
 * response-mapping logic (executeNoRegression/captureBaseline) without a
 * real orchestrator process -- the same boundary GrpcValidatorExecutor's
 * own class doc calls out as needing a real backend that doesn't exist
 * in this test's context.
 */
function withStubbedCall(
  responses:
    Record<string, unknown> | ((method: string, req: unknown) => unknown),
) {
  const executor = new GrpcValidatorExecutor(undefined as never);
  const calls: Array<{ method: string; request: unknown }> = [];
  (executor as unknown as { call: unknown }).call = async (
    method: string,
    request: unknown,
  ) => {
    calls.push({ method, request });
    if (typeof responses === 'function') return responses(method, request);
    if (method in responses) return responses[method];
    throw new Error(`no stub for ${method}`);
  };
  return { executor, calls };
}

function spec(overrides: Partial<ValidatorSpec> = {}): ValidatorSpec {
  return {
    id: 'v.no-regression',
    type: 'NO_REGRESSION',
    run: 'baseline.other-services',
    expect: {},
    weight: 1,
    ...overrides,
  };
}

describe('GrpcValidatorExecutor NO_REGRESSION dispatch', () => {
  it('routes NO_REGRESSION through CheckRegression, not ExecValidator', async () => {
    const { executor, calls } = withStubbedCall({
      CheckRegression: { regressionFound: false, regressedResources: [] },
    });

    await executor.execute('env-1', 'attempt-1', spec());

    expect(calls).toHaveLength(1);
    expect(calls[0].method).toBe('CheckRegression');
    expect(calls[0].request).toEqual({
      environmentId: 'env-1',
      snapshotKey: 'baseline.other-services',
    });
  });

  it('maps regressionFound=false to PASS', async () => {
    const { executor } = withStubbedCall({
      CheckRegression: { regressionFound: false, regressedResources: [] },
    });
    const result = await executor.execute('env-1', 'attempt-1', spec());
    expect(result.status).toBe('PASS');
    expect(result.validatorId).toBe('v.no-regression');
  });

  it('maps regressionFound=true to FAIL and surfaces the regressed resources as observed', async () => {
    const { executor } = withStubbedCall({
      CheckRegression: {
        regressionFound: true,
        regressedResources: [
          'Deployment/payments-api: readyReplicas dropped 3 -> 0',
        ],
      },
    });
    const result = await executor.execute('env-1', 'attempt-1', spec());
    expect(result.status).toBe('FAIL');
    expect(result.observed).toEqual({
      regressed_resources: [
        'Deployment/payments-api: readyReplicas dropped 3 -> 0',
      ],
    });
  });

  it('returns ERROR (not a thrown exception) when no baseline was captured', async () => {
    const { executor } = withStubbedCall(() => {
      throw Object.assign(new Error('no baseline found'), { code: 5 }); // gRPC NOT_FOUND
    });
    const result = await executor.execute('env-1', 'attempt-1', spec());
    expect(result.status).toBe('ERROR');
    expect(result.evidenceRef).toMatch(/no baseline found/);
  });

  it('returns ERROR without calling the RPC at all when the validator spec has no snapshot_key', async () => {
    const { executor, calls } = withStubbedCall({});
    const result = await executor.execute(
      'env-1',
      'attempt-1',
      spec({ run: undefined }),
    );
    expect(result.status).toBe('ERROR');
    expect(result.evidenceRef).toMatch(/snapshot_key/);
    expect(calls).toHaveLength(0);
  });

  it('captureBaseline maps CaptureBaseline response fields', async () => {
    const { executor, calls } = withStubbedCall({
      CaptureBaseline: {
        snapshotKey: 'baseline.other-services',
        capturedAt: '2026-01-01T00:00:00Z',
        resourcesCaptured: 4,
      },
    });

    const result = await executor.captureBaseline(
      'env-1',
      'baseline.other-services',
    );

    expect(calls[0]).toEqual({
      method: 'CaptureBaseline',
      request: {
        environmentId: 'env-1',
        snapshotKey: 'baseline.other-services',
      },
    });
    expect(result).toEqual({
      snapshotKey: 'baseline.other-services',
      capturedAt: '2026-01-01T00:00:00Z',
      resourcesCaptured: 4,
    });
  });

  it('non-NO_REGRESSION validator types still route through ExecValidator', async () => {
    const { executor, calls } = withStubbedCall({
      ExecValidator: { status: 'PASS', observedJson: '{}', durationMs: 5 },
    });
    await executor.execute(
      'env-1',
      'attempt-1',
      spec({ id: 'v.shell', type: 'SHELL_ASSERT', run: 'true' }),
    );
    expect(calls[0].method).toBe('ExecValidator');
  });

  // PLAN_RPC_AUTHZ.md Section 4/5: ExecValidator's ownership check
  // (orchestrator/internal/orchestrator/server.go) is worthless if the
  // client never actually sends attempt_id -- this asserts the outgoing
  // request payload really carries it, not just that execute() accepts
  // the parameter.
  it('forwards attemptId in the ExecValidator request payload', async () => {
    const { executor, calls } = withStubbedCall({
      ExecValidator: { status: 'PASS', observedJson: '{}', durationMs: 5 },
    });
    await executor.execute(
      'env-1',
      'attempt-42',
      spec({ id: 'v.shell', type: 'SHELL_ASSERT', run: 'true' }),
    );
    expect(calls[0].method).toBe('ExecValidator');
    expect(calls[0].request).toMatchObject({ attemptId: 'attempt-42' });
  });
});
