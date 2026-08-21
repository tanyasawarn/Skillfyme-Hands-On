import { Injectable, Logger } from '@nestjs/common';
import type {
  ValidatorExecutionResult,
  ValidatorExecutor,
  ValidatorSpec,
} from './validator-executor.interface';

/**
 * PLAN.md Phase 0 pattern applied to validation: a fake executor stands
 * in for real environment access until Dev A's Orchestrator can mint
 * credentials into a live environment (§6.2). Unlike FakeOrchestratorClient
 * (always succeeds), this fake needs controllable PASS/FAIL/ERROR outcomes
 * -- the scoring engine's whole job is to react differently to different
 * validator results, so an executor that always passes would make the
 * scoring engine untestable against realistic mixed outcomes.
 *
 * Outcome is controlled via an in-memory override map keyed by
 * `${environmentId}:${validatorId}` -- environmentId, not attemptId,
 * because that's the only identifier this.execute() actually receives
 * (matching the real ValidatorExecutor contract, which is scoped to one
 * environment, not one attempt -- see §6.2: credentials are "scoped to
 * that one environment"). Callers (tests, or a future dev "force a
 * validator result" admin tool) set overrides via setOverride() using
 * the same environmentId the attempt was provisioned with. Validators
 * with no override default to PASS, so a normal happy-path flow doesn't
 * require every validator to be pre-configured.
 */
@Injectable()
export class FakeValidatorExecutor implements ValidatorExecutor {
  private readonly logger = new Logger(FakeValidatorExecutor.name);
  private readonly overrides = new Map<string, 'PASS' | 'FAIL' | 'ERROR'>();

  setOverride(
    environmentId: string,
    validatorId: string,
    status: 'PASS' | 'FAIL' | 'ERROR',
  ) {
    this.overrides.set(`${environmentId}:${validatorId}`, status);
  }

  clearOverrides(environmentId: string) {
    for (const key of [...this.overrides.keys()]) {
      if (key.startsWith(`${environmentId}:`)) this.overrides.delete(key);
    }
  }

  async execute(
    environmentId: string,
    spec: ValidatorSpec,
  ): Promise<ValidatorExecutionResult> {
    const start = Date.now();
    const status = this.overrides.get(`${environmentId}:${spec.id}`) ?? 'PASS';

    this.logger.debug(
      `[fake] executing ${spec.id} (${spec.type}) on ${environmentId} -> ${status}`,
    );
    await sleep(5);

    if (status === 'ERROR') {
      // Doc §6.2: "ERROR (validator itself broke) is never scored against
      // the learner." No observed/expected -- the check itself failed to run.
      return {
        validatorId: spec.id,
        status: 'ERROR',
        durationMs: Date.now() - start,
      };
    }

    return {
      validatorId: spec.id,
      status,
      observed:
        status === 'PASS'
          ? spec.expect
          : { note: 'fake executor: simulated failure' },
      expected: spec.expect,
      durationMs: Date.now() - start,
    };
  }
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}
