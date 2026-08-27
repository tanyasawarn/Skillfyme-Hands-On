/**
 * Doc §6.2: "Validators run in a Validator Runner -- a separate pod/job
 * that the learner's environment cannot see, reach, or influence. It
 * holds read-only credentials... minted per run with a 5-minute
 * lifetime." That execution boundary (mint creds via Dev A's
 * Orchestrator, exec inside/against the environment, revoke creds) is
 * infrastructure ValidatorRunnerService does not yet have a real backend
 * for -- same situation as OrchestratorClient in Phase 0/1: Dev A's
 * environments don't exist yet, so this interface is the swappable
 * boundary. FakeValidatorExecutor stands in until a real executor can
 * mint credentials via ORCHESTRATOR_CLIENT.MintValidatorCredentials
 * (contracts/orchestrator.proto) and dispatch into a live environment.
 */
export interface ValidatorSpec {
  id: string;
  type:
    | 'SHELL_ASSERT'
    | 'SHELL_JSON'
    | 'FILE_EXISTS'
    | 'FILE_CONTENT'
    | 'FILE_PARSE'
    | 'K8S_ASSERT'
    | 'K8S_EVENT_ABSENT'
    | 'HTTP_PROBE'
    | 'HTTP_SLO'
    | 'CLOUD_ASSERT'
    | 'IAC_STATE'
    | 'DB_QUERY'
    | 'TEST_SUITE'
    | 'STATIC_ANALYSIS'
    | 'PERF_BENCH'
    | 'CHAOS_PROBE'
    | 'TELEMETRY_ASSERT'
    | 'NO_REGRESSION'
    | 'AI_RUBRIC';
  run?: string;
  expect: Record<string, unknown>;
  weight: number;
  severity?: 'BLOCKING' | 'WARN';
  timeoutMs?: number;
}

export type ValidatorResultStatus = 'PASS' | 'FAIL' | 'ERROR' | 'SKIP';

export interface ValidatorExecutionResult {
  validatorId: string;
  status: ValidatorResultStatus;
  observed?: unknown;
  expected?: unknown;
  durationMs: number;
  evidenceRef?: string;
}

export const VALIDATOR_EXECUTOR = Symbol('VALIDATOR_EXECUTOR');

export interface ValidatorExecutor {
  /**
   * Executes one validator against the given environment. Doc §6.2:
   * "Every validator declares a timeout and retry policy... A flaky
   * validator is worse than no validator" -- retry/backoff is the
   * executor's responsibility, not the caller's, since only the executor
   * knows what "eventual consistency" means for a given check type.
   *
   * attemptId closes the same access-control gap InjectFaultRequest's
   * attemptId field closed (PHASE2_CLOSEOUT.md): the real executor
   * (GrpcValidatorExecutor) forwards this to the orchestrator's
   * ExecValidator RPC, which verifies it matches env.environment.attempt_id
   * for environmentId before running anything. Required.
   */
  execute(
    environmentId: string,
    attemptId: string,
    spec: ValidatorSpec,
  ): Promise<ValidatorExecutionResult>;
}
