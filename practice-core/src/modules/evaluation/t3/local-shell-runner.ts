import { Injectable, Logger } from '@nestjs/common';
import { spawn } from 'node:child_process';
import * as path from 'node:path';
import * as fs from 'node:fs';
import type { ShellCommand, ShellResult, ShellRunner } from './shell-runner';

/**
 * Phase 3 (1.8 / B3 de-risking path). Runs validator commands on the
 * Validator Runner host, rooted at `rootDir`. Used to build and test
 * IAC_STATE / CLOUD_ASSERT / STATIC_ANALYSIS against a static Terraform
 * fixture repo + a pre-made sandbox account, before the T3 driver exists.
 *
 * Not for production T3: a real learner's infra state and cloud calls go
 * through OrchestratorShellRunner (inside the brokered-credential pod).
 * This runner is selected only when T3_LOCAL_FIXTURE_DIR is set — see
 * evaluation.module.ts.
 *
 * Safety: argv only (no shell string), cwd is confined to rootDir (a
 * `..` escape is rejected), and every run has a hard timeout that kills
 * the process group.
 */
@Injectable()
export class LocalShellRunner implements ShellRunner {
  readonly backend = 'local-fixture';
  private readonly logger = new Logger(LocalShellRunner.name);
  private readonly defaultTimeoutMs = 120_000;

  constructor(private readonly rootDir: string) {
    if (!path.isAbsolute(rootDir)) {
      throw new Error(`LocalShellRunner rootDir must be absolute: ${rootDir}`);
    }
  }

  private resolveCwd(cwd?: string): string {
    const resolved = path.resolve(this.rootDir, cwd ?? '.');
    const rel = path.relative(this.rootDir, resolved);
    if (rel.startsWith('..') || path.isAbsolute(rel)) {
      throw new Error(
        `LocalShellRunner: cwd "${cwd}" escapes the fixture root ${this.rootDir}`,
      );
    }
    return resolved;
  }

  async run(cmd: ShellCommand): Promise<ShellResult> {
    const start = Date.now();
    let cwd: string;
    try {
      cwd = this.resolveCwd(cmd.cwd);
    } catch (err) {
      return {
        exitCode: -1,
        stdout: '',
        stderr: '',
        infraError: err instanceof Error ? err.message : String(err),
        durationMs: Date.now() - start,
      };
    }
    if (!fs.existsSync(cwd)) {
      return {
        exitCode: -1,
        stdout: '',
        stderr: '',
        infraError: `working directory does not exist: ${cwd}`,
        durationMs: Date.now() - start,
      };
    }

    const [bin, ...args] = cmd.argv;
    if (!bin) {
      return {
        exitCode: -1,
        stdout: '',
        stderr: '',
        infraError: 'empty argv',
        durationMs: Date.now() - start,
      };
    }
    const timeoutMs = cmd.timeoutMs ?? this.defaultTimeoutMs;

    return await new Promise<ShellResult>((resolve) => {
      const child = spawn(bin, args, {
        cwd,
        env: { ...process.env, ...(cmd.env ?? {}) },
        stdio: ['ignore', 'pipe', 'pipe'],
      });

      let stdout = '';
      let stderr = '';
      let settled = false;
      const done = (r: ShellResult) => {
        if (settled) return;
        settled = true;
        clearTimeout(timer);
        resolve(r);
      };

      const timer = setTimeout(() => {
        child.kill('SIGKILL');
        done({
          exitCode: -1,
          stdout,
          stderr,
          infraError: `command timed out after ${timeoutMs}ms: ${bin} ${args.join(' ')}`,
          durationMs: Date.now() - start,
        });
      }, timeoutMs);

      child.stdout.on('data', (d: Buffer) => {
        stdout += d.toString();
      });
      child.stderr.on('data', (d: Buffer) => {
        stderr += d.toString();
      });
      child.on('error', (err) => {
        // ENOENT etc — the binary isn't installed on the runner host.
        done({
          exitCode: -1,
          stdout,
          stderr,
          infraError: `failed to start "${bin}": ${err.message}`,
          durationMs: Date.now() - start,
        });
      });
      child.on('close', (code) => {
        done({
          exitCode: code ?? -1,
          stdout,
          stderr,
          durationMs: Date.now() - start,
        });
      });
    });
  }
}
