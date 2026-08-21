import { BadRequestException, Inject, Injectable } from '@nestjs/common';
import { AttemptRepository } from './attempt.repository';
import {
  ORCHESTRATOR_CLIENT,
  type OrchestratorClient,
} from './orchestrator-client.interface';

export interface WorkspaceFileEntry {
  path: string; // relative to the workspace home dir, e.g. "project/deploy.sh"
  type: 'file' | 'dir';
}

// homeDir matches the workspace pod's actual home directory (the
// non-root `ubuntu` user PodSecurity `restricted` enforcement requires,
// confirmed live earlier this session -- root-owned /root is not
// writable by that user). All file paths this service accepts/returns
// are relative to this, never absolute, so the editor can't be pointed
// at arbitrary pod paths outside the learner's own workspace.
const homeDir = '/home/ubuntu';

/**
 * Doc §8.5's Monaco editor is explicitly "client-side, file-API-backed"
 * (ConnectResult.editorUrl is always "" -- there's no server-rendered
 * editor). This service is that file API's server side: it runs shell
 * commands inside the workspace pod via the orchestrator's ExecShell RPC
 * (OrchestratorClient.execShell, the same primitive the content-CI
 * golden-path harness uses to apply reference solutions) to list, read,
 * and write files.
 *
 * File content crosses the wire as base64, both directions -- content
 * shown in Monaco can contain arbitrary bytes, shell-special characters,
 * and unbalanced quotes, and this session already hit one real bug from
 * naive shell quoting (orchestrator/internal/validation's shellQuote
 * tilde-expansion fix); base64-encoding the payload sidesteps that whole
 * class of problem rather than trying to shell-escape file content
 * correctly.
 */
@Injectable()
export class WorkspaceFileService {
  constructor(
    private readonly attempts: AttemptRepository,
    @Inject(ORCHESTRATOR_CLIENT)
    private readonly orchestrator: OrchestratorClient,
  ) {}

  async list(
    attemptId: string,
    relativeDir: string,
  ): Promise<WorkspaceFileEntry[]> {
    const environmentId = await this.environmentIdFor(attemptId);
    const dir = resolveRelativePath(relativeDir || '.');

    // -mindepth 1 -maxdepth 1: direct children only, not the full
    // recursive tree -- the frontend expands directories lazily, one
    // list() call per expansion, matching how a real file-tree UI works
    // rather than shipping the whole workspace on first load.
    const command = `find "${dir}" -mindepth 1 -maxdepth 1 \\( -type d -printf '%f/\\n' -o -type f -printf '%f\\n' \\) 2>/dev/null | sort`;
    const result = await this.orchestrator.execShell({
      environmentId,
      command,
      timeoutMs: 10_000,
    });
    if (result.errorMessage) {
      throw new BadRequestException(
        `listing ${relativeDir || '.'}: ${result.errorMessage}`,
      );
    }
    if (result.exitCode !== 0) {
      return []; // directory doesn't exist yet (e.g. a task the learner hasn't started) -- empty, not an error
    }

    return result.stdout
      .split('\n')
      .filter((line) => line.length > 0)
      .map((line) => {
        const isDir = line.endsWith('/');
        const name = isDir ? line.slice(0, -1) : line;
        const path =
          relativeDir && relativeDir !== '.' ? `${relativeDir}/${name}` : name;
        return { path, type: isDir ? 'dir' : 'file' };
      });
  }

  async read(
    attemptId: string,
    relativePath: string,
  ): Promise<{ content: string }> {
    const environmentId = await this.environmentIdFor(attemptId);
    const path = resolveRelativePath(relativePath);

    const result = await this.orchestrator.execShell({
      environmentId,
      command: `base64 "${path}" 2>&1`,
      timeoutMs: 10_000,
    });
    if (result.errorMessage) {
      throw new BadRequestException(
        `reading ${relativePath}: ${result.errorMessage}`,
      );
    }
    if (result.exitCode !== 0) {
      throw new BadRequestException(
        `${relativePath}: not found or not readable`,
      );
    }

    return {
      content: Buffer.from(result.stdout.replace(/\n/g, ''), 'base64').toString(
        'utf-8',
      ),
    };
  }

  async write(
    attemptId: string,
    relativePath: string,
    content: string,
  ): Promise<void> {
    const environmentId = await this.environmentIdFor(attemptId);
    const path = resolveRelativePath(relativePath);
    const encoded = Buffer.from(content, 'utf-8').toString('base64');

    // mkdir -p the parent first -- Monaco can create a new file inside a
    // directory that doesn't exist on disk yet (a learner typing a path
    // for a file they haven't created), and `base64 -d >` doesn't create
    // intermediate directories on its own.
    const command = `mkdir -p "$(dirname "${path}")" && echo '${encoded}' | base64 -d > "${path}"`;
    const result = await this.orchestrator.execShell({
      environmentId,
      command,
      timeoutMs: 10_000,
    });
    if (result.errorMessage) {
      throw new BadRequestException(
        `writing ${relativePath}: ${result.errorMessage}`,
      );
    }
    if (result.exitCode !== 0) {
      throw new BadRequestException(
        `writing ${relativePath}: ${result.stderr || 'unknown error'}`,
      );
    }
  }

  private async environmentIdFor(attemptId: string): Promise<string> {
    const attempt = await this.attempts.findById(attemptId);
    if (!attempt)
      throw new BadRequestException(`attempt ${attemptId} not found`);
    if (!attempt.environment_id) {
      throw new BadRequestException(
        `attempt ${attemptId} has no environment (status ${attempt.status})`,
      );
    }
    return attempt.environment_id;
  }
}

// resolveRelativePath rejects any attempt to escape homeDir (`..`
// segments, absolute paths) -- doc §9.1's T3 cross-learner-access threat
// applies here too: a path like "../../etc/passwd" must not resolve
// outside this attempt's own workspace pod home directory. Since each
// attempt has its own pod/namespace already (doc §5.2), this isn't
// cross-*learner* access exactly, but it's still not a boundary the
// editor API should let a client-supplied path cross.
function resolveRelativePath(relativePath: string): string {
  const segments = relativePath
    .split('/')
    .filter((s) => s.length > 0 && s !== '.');
  if (segments.some((s) => s === '..')) {
    throw new BadRequestException(`invalid path: ${relativePath}`);
  }
  return [homeDir, ...segments].join('/');
}
