/**
 * Phase 3 (PLAN_PHASE3_PROJECTS.md 1.8 / B3). The command-execution
 * boundary the three early T3 validator executors (IAC_STATE,
 * CLOUD_ASSERT, STATIC_ANALYSIS) sit on top of.
 *
 * Two implementations:
 *
 *  - OrchestratorShellRunner — runs the command inside the learner's T3
 *    workspace pod via the orchestrator's ExecShell RPC (the real path
 *    once the T3 driver, Stage 3.2, exists). Credentials are already
 *    brokered into that pod; the Validator Runner never holds them.
 *
 *  - LocalShellRunner — runs the command on the Validator Runner host
 *    itself, rooted at a working directory. This is the de-risking path
 *    the plan calls out (B3 note): "CLOUD_ASSERT / IAC_STATE /
 *    STATIC_ANALYSIS can be built and tested against a static Terraform
 *    repo + a pre-made sandbox account before the full driver exists."
 *
 * Both return the same shape, so a validator executor written against
 * this interface does not know or care which backend ran the command.
 */

export interface ShellCommand {
  /** argv — never a shell string; the runner is responsible for safe exec. */
  argv: string[];
  /** working directory, relative to the runner's root (workspace pod cwd, or LocalShellRunner's rootDir). */
  cwd?: string;
  /** extra env for this command only (e.g. AWS_REGION); merged over the runner's base env. */
  env?: Record<string, string>;
  timeoutMs?: number;
}

export interface ShellResult {
  exitCode: number;
  stdout: string;
  stderr: string;
  /** set only on an infrastructure failure to run the command at all (pod unreachable, binary missing) — NOT a non-zero exit. */
  infraError?: string;
  durationMs: number;
}

export const T3_SHELL_RUNNER = Symbol('T3_SHELL_RUNNER');

export interface ShellRunner {
  /**
   * Runs one command. Never throws for a non-zero exit or a command that
   * produced stderr — those are normal results the caller interprets.
   * Only a genuine inability to execute (transport failure, missing
   * binary, timeout) sets `infraError`, which the caller maps to a
   * validator ERROR (doc §6.2: never scored against the learner).
   */
  run(cmd: ShellCommand): Promise<ShellResult>;

  /** Human-readable name of the backend, for evidence/log lines. */
  readonly backend: string;
}
