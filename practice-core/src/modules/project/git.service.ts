import { Injectable, Logger } from '@nestjs/common';
import { ForgejoClient } from './forgejo.client';
import { ProjectRepository } from './project.repository';
import type { ProjectMilestoneKey } from '../../db/schema';

/**
 * Phase 3 (PLAN_PHASE3_PROJECTS.md 1.7 / B9). The practice-core side of
 * per-learner platform Git hosting.
 *
 * On project enrol:
 *   - provision the learner's org + project repo on the platform Forgejo
 *     (1.4), seeded with the activity's requirements pack
 *   - grant the learner a repo-scoped push token
 *
 * Per milestone submission:
 *   - record `project_submission.repo_ref` + `commit_sha` (the exact
 *     commit the validators ran against)
 *
 * For the milestone-5 viva generator (Stage 3.8):
 *   - read commit history, file content at a commit, and diffs
 *
 * Degrades cleanly: if FORGEJO_BASE_URL / FORGEJO_ADMIN_TOKEN are not
 * configured (local dev without the git-hosting profile), every method
 * that would call out logs and returns a null-ish result instead of
 * throwing, so the milestone state machine (1.6) still runs against a
 * fake. `isEnabled()` lets callers branch explicitly.
 */

export interface EnrolResult {
  enabled: boolean;
  repoRef: string | null; // "forgejo:<org>/<repo>"
  cloneUrl: string | null;
  htmlUrl: string | null;
  /** repo-scoped push token — returned once, handed to the learner, not stored. */
  pushToken: string | null;
}

export interface RequirementsFile {
  path: string; // repo-relative, e.g. "REQUIREMENTS.md" or "docs/spec.md"
  content: string;
}

// "forgejo:org/repo" <-> {org, repo}
function parseRepoRef(repoRef: string): { org: string; repo: string } {
  const m = /^forgejo:([^/]+)\/(.+)$/.exec(repoRef);
  if (!m) throw new Error(`unrecognised repo_ref: ${repoRef}`);
  return { org: m[1], repo: m[2] };
}
function toRepoRef(org: string, repo: string): string {
  return `forgejo:${org}/${repo}`;
}

@Injectable()
export class GitService {
  private readonly logger = new Logger(GitService.name);

  constructor(
    private readonly forgejo: ForgejoClient,
    private readonly projects: ProjectRepository,
  ) {}

  isEnabled(): boolean {
    return this.forgejo.isConfigured();
  }

  /**
   * Provision + seed the learner's repo for a project attempt. Idempotent:
   * re-enrolling the same attempt returns the existing repo and a fresh
   * push token (old tokens stay valid).
   *
   * @param learner   { userId, username, email } — username/email used for the Forgejo user + org naming
   * @param project   { slug } — the activity slug, used in the repo name
   * @param attemptId used only in the token name for traceability
   * @param requirements files seeded into the repo's default branch on first creation
   */
  async enrolProject(input: {
    attemptId: string;
    learner: { userId: string; username: string; email: string };
    project: { slug: string };
    requirements: RequirementsFile[];
  }): Promise<EnrolResult> {
    if (!this.isEnabled()) {
      this.logger.warn(
        `enrolProject(${input.attemptId}): Forgejo not configured — skipping repo provisioning`,
      );
      return {
        enabled: false,
        repoRef: null,
        cloneUrl: null,
        htmlUrl: null,
        pushToken: null,
      };
    }

    const org = orgNameFor(input.learner.username);
    const repo = repoNameFor(input.project.slug);
    const branch = 'main';

    const learnerAccount = await this.forgejo.ensureUser(
      input.learner.username,
      input.learner.email,
    );
    await this.forgejo.ensureOrg(org);
    const created = await this.forgejo.ensureRepo(org, repo, {
      autoInit: true,
      defaultBranch: branch,
      description: `Project attempt ${input.attemptId} — ${input.project.slug}`,
    });

    // Seed the requirements pack only if the repo was freshly initialised
    // with just the auto-init commit (avoid clobbering learner work on a
    // re-enrol). We detect "fresh" by checking whether REQUIREMENTS files
    // already exist; a missing file → create it.
    for (const f of input.requirements) {
      let exists = false;
      try {
        await this.forgejo.getFileContent(org, repo, f.path, branch);
        exists = true;
      } catch {
        exists = false;
      }
      if (!exists) {
        await this.forgejo.putFile(org, repo, f.path, f.content, {
          branch,
          message: `seed: ${f.path}`,
        });
      }
    }

    await this.forgejo.addRepoCollaborator(org, repo, input.learner.username);
    const pushToken = await this.forgejo.createUserScopedToken(
      input.learner.username,
      learnerAccount.password,
      `push-${input.attemptId}-${Date.now()}`,
    );

    void created; // ensureRepo already returned the repo; kept for clarity
    const repoMeta = await this.forgejo.getRepo(org, repo);
    return {
      enabled: true,
      repoRef: toRepoRef(org, repo),
      cloneUrl: repoMeta.cloneUrl,
      htmlUrl: repoMeta.htmlUrl,
      pushToken,
    };
  }

  /**
   * Record which commit a milestone submission was graded against.
   * Resolves the current HEAD of the repo's default branch when
   * commitSha is not supplied by the caller.
   */
  async recordMilestoneSubmission(input: {
    attemptId: string;
    milestoneKey: ProjectMilestoneKey;
    repoRef: string;
    attemptNumber: number;
    commitSha?: string;
  }): Promise<{ commitSha: string }> {
    let commitSha = input.commitSha ?? '';
    if (!commitSha && this.isEnabled()) {
      try {
        const { org, repo } = parseRepoRef(input.repoRef);
        const commits = await this.forgejo.listCommits(org, repo, { limit: 1 });
        commitSha = commits[0]?.sha ?? '';
      } catch (e) {
        this.logger.warn(
          `recordMilestoneSubmission(${input.attemptId}/${input.milestoneKey}): could not resolve HEAD: ${
            e instanceof Error ? e.message : String(e)
          }`,
        );
      }
    }

    await this.projects.recordSubmission({
      attemptId: input.attemptId,
      milestoneKey: input.milestoneKey,
      repoRef: input.repoRef,
      commitSha,
      attemptNumber: input.attemptNumber,
    });
    return { commitSha };
  }

  /** Commit history for a repo_ref, newest first. Empty when Forgejo is off. */
  async listCommits(
    repoRef: string,
    opts: { limit?: number; sha?: string } = {},
  ): Promise<
    Array<{
      sha: string;
      message: string;
      authorName: string;
      authoredAt: string;
    }>
  > {
    if (!this.isEnabled()) return [];
    const { org, repo } = parseRepoRef(repoRef);
    return this.forgejo.listCommits(org, repo, opts);
  }

  /** A file's content at a ref (for the viva generator reading the design doc). */
  async readFile(
    repoRef: string,
    filePath: string,
    ref?: string,
  ): Promise<string | null> {
    if (!this.isEnabled()) return null;
    const { org, repo } = parseRepoRef(repoRef);
    try {
      return await this.forgejo.getFileContent(org, repo, filePath, ref);
    } catch {
      return null;
    }
  }

  /** Unified diff base…head (for the viva generator's design-vs-implementation probes). */
  async diff(
    repoRef: string,
    base: string,
    head: string,
  ): Promise<string | null> {
    if (!this.isEnabled()) return null;
    const { org, repo } = parseRepoRef(repoRef);
    try {
      return await this.forgejo.getDiff(org, repo, base, head);
    } catch {
      return null;
    }
  }

  /**
   * Retention: the learner's repo is deliberately NOT deleted when a
   * project attempt ends (portfolio value — memory.md §12.3). This method
   * exists only for test cleanup and admin tooling; the state machine
   * never calls it.
   */
  async deleteLearnerRepo(repoRef: string): Promise<void> {
    if (!this.isEnabled()) return;
    const { org, repo } = parseRepoRef(repoRef);
    await this.forgejo.deleteRepo(org, repo);
  }
}

function orgNameFor(username: string): string {
  // Forgejo org/user names: alnum, dash, dot, underscore; <= 40 chars.
  return `learner-${slug(username)}`.slice(0, 40);
}
function repoNameFor(projectSlug: string): string {
  return `proj-${slug(projectSlug)}`.slice(0, 100);
}
function slug(s: string): string {
  return s
    .toLowerCase()
    .replace(/[^a-z0-9._-]+/g, '-')
    .replace(/^-+|-+$/g, '');
}
