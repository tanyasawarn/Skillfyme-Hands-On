import { Injectable, Logger } from '@nestjs/common';
import { ConfigService } from '@nestjs/config';
import { BaseGrpcClient } from '../../../common/base-grpc-client';
import type { ShellCommand, ShellResult, ShellRunner } from './shell-runner';

/**
 * Phase 3 (1.8 / B3). The production path: runs a validator command
 * inside the learner's T3 workspace pod via the orchestrator's ExecShell
 * RPC (contracts/orchestrator.proto). Credentials are already brokered
 * into that pod by the STS sidecar (Stage 2.1) — the Validator Runner
 * itself never holds cloud creds, matching doc §6.2's isolation.
 *
 * A second BaseGrpcClient subclass (like GrpcValidatorExecutor) rather
 * than reusing the attempt module's OrchestratorClient: evaluation must
 * not import attempt (circular — attempt imports evaluation). The
 * connection plumbing is shared via BaseGrpcClient.
 *
 * ExecShell needs an attempt_id for its ownership check. That is not on
 * ShellCommand (which is backend-agnostic); it is bound per validator
 * run via `forAttempt()`, which returns a thin per-run view. The
 * executor calls forAttempt(environmentId, attemptId) once and runs all
 * of a validator's commands through the returned runner.
 */
@Injectable()
export class OrchestratorShellRunner
  extends BaseGrpcClient
  implements ShellRunner
{
  readonly backend = 'orchestrator-exec-shell';
  protected readonly logger = new Logger(OrchestratorShellRunner.name);
  protected readonly protoFile = 'orchestrator.proto';
  protected readonly protoServicePath =
    'practiceengine.orchestrator.v1.EnvironmentOrchestrator';

  constructor(config: ConfigService) {
    super(config);
  }

  protected connectionLogMessage(address: string): string {
    return `T3 shell runner connecting to Environment Orchestrator at ${address}`;
  }

  /**
   * Returns a runner bound to one (environment, attempt) for the
   * duration of a validator run. The returned object is a shallow view —
   * it shares this instance's gRPC channel but carries its own ctx, so
   * concurrent validator runs on different attempts don't clobber each
   * other.
   */
  forAttempt(environmentId: string, attemptId: string): ShellRunner {
    return {
      backend: this.backend,
      run: (cmd: ShellCommand): Promise<ShellResult> =>
        this.runBound(environmentId, attemptId, cmd),
    };
  }

  /** Direct run() without forAttempt() is a programming error — the ownership id is required. */
  run(): Promise<ShellResult> {
    return Promise.resolve({
      exitCode: -1,
      stdout: '',
      stderr: '',
      infraError:
        'OrchestratorShellRunner.run() called without forAttempt(environmentId, attemptId) — bind the run context first',
      durationMs: 0,
    });
  }

  private async runBound(
    environmentId: string,
    attemptId: string,
    cmd: ShellCommand,
  ): Promise<ShellResult> {
    const start = Date.now();
    // ExecShell takes a single command string run via `/bin/bash -c`.
    // Turn argv into a safely-quoted string; env vars become a prefix.
    const envPrefix = Object.entries(cmd.env ?? {})
      .map(([k, v]) => `${k}=${shQuote(v)}`)
      .join(' ');
    const argvStr = cmd.argv.map(shQuote).join(' ');
    const cdPrefix = cmd.cwd ? `cd ${shQuote(cmd.cwd)} && ` : '';
    const command = `${cdPrefix}${envPrefix ? envPrefix + ' ' : ''}${argvStr}`;

    try {
      const response = await this.call<
        {
          environmentId: string;
          command: string;
          timeoutMs: number;
          attemptId: string;
        },
        {
          exitCode?: number;
          stdout?: string;
          stderr?: string;
          errorMessage?: string;
          durationMs?: number;
        }
      >(
        'ExecShell',
        {
          environmentId,
          command,
          timeoutMs: cmd.timeoutMs ?? 0,
          attemptId,
        },
        (cmd.timeoutMs ?? 120_000) + 5_000,
      );

      return {
        exitCode: response.exitCode ?? -1,
        stdout: response.stdout ?? '',
        stderr: response.stderr ?? '',
        // proto: error_message is set only on an infra-level failure, not a non-zero exit.
        infraError: response.errorMessage || undefined,
        durationMs: response.durationMs ?? Date.now() - start,
      };
    } catch (err) {
      const message = err instanceof Error ? err.message : String(err);
      this.logger.warn(`ExecShell failed for env=${environmentId}: ${message}`);
      return {
        exitCode: -1,
        stdout: '',
        stderr: '',
        infraError: message,
        durationMs: Date.now() - start,
      };
    }
  }
}

/** POSIX single-quote quoting: wrap in '...' and escape embedded quotes. */
function shQuote(s: string): string {
  return `'${s.replace(/'/g, `'\\''`)}'`;
}
