import { Inject, Injectable, Logger, Optional } from '@nestjs/common';
import {
  VALIDATOR_EXECUTOR,
  type ValidatorExecutionResult,
  type ValidatorExecutor,
  type ValidatorSpec,
} from '../validator-executor.interface';
import { GrpcValidatorExecutor } from '../grpc-validator-executor';
import { T3_SHELL_RUNNER, type ShellRunner } from './shell-runner';
import { OrchestratorShellRunner } from './orchestrator-shell-runner';
import { runIacState } from './iac-state.executor';
import { runStaticAnalysis } from './static-analysis.executor';
import { runCloudAssert } from './cloud-assert.executor';
import { runTestSuite } from './test-suite.executor';
import { runPerfBench } from './perf-bench.executor';
import { runChaosProbe } from './chaos-probe.executor';

/**
 * Phase 3 (PLAN_PHASE3_PROJECTS.md 1.8 / B3). The client-side dispatch
 * layer for the three early T3 validator types. Implements the same
 * ValidatorExecutor contract as FakeValidatorExecutor / GrpcValidator-
 * Executor, so ValidatorRunnerService is unchanged — it still calls
 * `execute(environmentId, attemptId, spec)` and still filters ERROR out
 * of scoring (doc §6.2).
 *
 *  - IAC_STATE / CLOUD_ASSERT / STATIC_ANALYSIS → the typed handlers in
 *    this directory, run through a ShellRunner (OrchestratorShellRunner
 *    in production, LocalShellRunner against a static fixture for the
 *    pre-driver de-risking the plan calls out).
 *  - Everything else → delegated verbatim to GrpcValidatorExecutor
 *    (the SHELL / FILE / K8S_ASSERT / NO_REGRESSION types today;
 *    TEST_SUITE / CHAOS_PROBE / PERF_BENCH land in Stage 3.7).
 *
 * This is registered as the VALIDATOR_EXECUTOR in evaluation.module.ts
 * whenever the T3 path is enabled; it falls back to `delegate` for
 * every non-T3 type so nothing regresses.
 */
@Injectable()
export class T3ValidatorExecutor implements ValidatorExecutor {
  private readonly logger = new Logger(T3ValidatorExecutor.name);

  constructor(
    private readonly delegate: GrpcValidatorExecutor,
    @Optional()
    @Inject(T3_SHELL_RUNNER)
    private readonly shellRunner?: ShellRunner,
  ) {}

  async execute(
    environmentId: string,
    attemptId: string,
    spec: ValidatorSpec,
  ): Promise<ValidatorExecutionResult> {
    if (
      !T3_VALIDATOR_TYPES.includes(
        spec.type as (typeof T3_VALIDATOR_TYPES)[number],
      )
    ) {
      return this.delegate.execute(environmentId, attemptId, spec);
    }

    const start = Date.now();
    const runner = this.resolveRunner(environmentId, attemptId);
    if (!runner) {
      // No shell runner wired at all — treat as ERROR (platform gap,
      // never scored against the learner) rather than silently passing.
      this.logger.warn(
        `T3 validator ${spec.id} (${spec.type}) requested but no T3_SHELL_RUNNER is configured`,
      );
      return {
        validatorId: spec.id,
        status: 'ERROR',
        durationMs: Date.now() - start,
        evidenceRef:
          'no T3 shell runner configured (set T3_LOCAL_FIXTURE_DIR for the fixture path, or wire the orchestrator runner)',
      };
    }

    const cfg = this.pickConfig(spec);
    try {
      switch (spec.type) {
        case 'IAC_STATE':
          return await runIacState(runner, spec.id, cfg.iac_state);
        case 'CLOUD_ASSERT':
          return await runCloudAssert(runner, spec.id, cfg.cloud_assert);
        case 'STATIC_ANALYSIS':
          return await runStaticAnalysis(runner, spec.id, cfg.static_analysis);
        case 'TEST_SUITE':
          return await runTestSuite(runner, spec.id, cfg.test_suite);
        case 'PERF_BENCH':
          return await runPerfBench(runner, spec.id, cfg.perf_bench);
        case 'CHAOS_PROBE':
          return await runChaosProbe(runner, spec.id, cfg.chaos_probe);
        default:
          // Unreachable — T3_VALIDATOR_TYPES gates the switch — but keeps
          // the function total for tsc.
          return this.delegate.execute(environmentId, attemptId, spec);
      }
    } catch (err) {
      // Any unexpected throw from a handler is ERROR, not FAIL.
      const message = err instanceof Error ? err.message : String(err);
      this.logger.error(
        `T3 validator ${spec.id} (${spec.type}) threw: ${message}`,
      );
      return {
        validatorId: spec.id,
        status: 'ERROR',
        durationMs: Date.now() - start,
        evidenceRef: message,
      };
    }
  }

  private resolveRunner(
    environmentId: string,
    attemptId: string,
  ): ShellRunner | undefined {
    if (!this.shellRunner) return undefined;
    // The orchestrator runner needs the (env, attempt) bound for its
    // ownership check; the local fixture runner ignores them.
    if (this.shellRunner instanceof OrchestratorShellRunner) {
      return this.shellRunner.forAttempt(environmentId, attemptId);
    }
    return this.shellRunner;
  }

  private pickConfig(spec: ValidatorSpec): {
    iac_state?: unknown;
    cloud_assert?: unknown;
    static_analysis?: unknown;
    test_suite?: unknown;
    perf_bench?: unknown;
    chaos_probe?: unknown;
  } {
    const c = spec.config ?? {};
    return {
      iac_state: c.iac_state,
      cloud_assert: c.cloud_assert,
      static_analysis: c.static_analysis,
      test_suite: c.test_suite,
      perf_bench: c.perf_bench,
      chaos_probe: c.chaos_probe,
    };
  }
}

// Re-export so evaluation.module.ts can wire the runner without importing
// three files.
export { OrchestratorShellRunner } from './orchestrator-shell-runner';
export { LocalShellRunner } from './local-shell-runner';
export { T3_SHELL_RUNNER } from './shell-runner';
export const T3_VALIDATOR_TYPES = [
  'IAC_STATE',
  'CLOUD_ASSERT',
  'STATIC_ANALYSIS',
  'TEST_SUITE',
  'PERF_BENCH',
  'CHAOS_PROBE',
] as const;

// Kept for symmetry with how VALIDATOR_EXECUTOR is imported elsewhere.
export { VALIDATOR_EXECUTOR };
