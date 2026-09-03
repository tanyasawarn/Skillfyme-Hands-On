import { Injectable, Logger } from '@nestjs/common';
import { ConfigService } from '@nestjs/config';
import { randomBytes } from 'node:crypto';

/**
 * Phase 3 (PLAN_PHASE3_PROJECTS.md 1.7 / B9). A thin typed client over the
 * Forgejo / Gitea REST API — the platform Git host provisioned by
 * infra/git-hosting/ (1.4). Only the endpoints GitService needs:
 * per-learner org + repo creation, seeding files, minting a scoped push
 * token, and reading commit history / file content / diffs (the last is
 * what the milestone-5 viva generator, Stage 3.8, consumes).
 *
 * Auth: an admin token (FORGEJO_ADMIN_TOKEN) with write:organization /
 * write:repository / write:user scope, created by
 * infra/git-hosting/scripts/bootstrap-admin.sh. Learner push access is a
 * per-repo scoped token minted via `createRepoScopedToken`, never the
 * admin token.
 *
 * Uses global fetch (Node >= 18). Every method throws ForgejoApiError on a
 * non-2xx so GitService can decide what is fatal vs recoverable.
 */

export class ForgejoApiError extends Error {
  constructor(
    readonly status: number,
    readonly method: string,
    readonly path: string,
    readonly body: string,
  ) {
    super(`Forgejo ${method} ${path} → ${status}: ${body.slice(0, 300)}`);
    this.name = 'ForgejoApiError';
  }
}

export interface ForgejoCommit {
  sha: string;
  message: string;
  authorName: string;
  authoredAt: string;
  htmlUrl: string;
}

export interface ForgejoRepo {
  fullName: string;
  cloneUrl: string;
  htmlUrl: string;
  defaultBranch: string;
  empty: boolean;
}

export const FORGEJO_CONFIGURED = 'FORGEJO_CONFIGURED';

@Injectable()
export class ForgejoClient {
  private readonly logger = new Logger(ForgejoClient.name);
  private readonly baseUrl: string;
  private readonly adminToken: string;
  private readonly timeoutMs: number;

  constructor(config: ConfigService) {
    this.baseUrl = (config.get<string>('FORGEJO_BASE_URL') ?? '').replace(
      /\/+$/,
      '',
    );
    this.adminToken = config.get<string>('FORGEJO_ADMIN_TOKEN') ?? '';
    this.timeoutMs = Number(
      config.get<string>('FORGEJO_TIMEOUT_MS') ?? '15000',
    );
  }

  /** True when both base URL and admin token are set — GitService no-ops otherwise. */
  isConfigured(): boolean {
    return this.baseUrl.length > 0 && this.adminToken.length > 0;
  }

  private async api<T>(
    method: string,
    path: string,
    body?: unknown,
    token = this.adminToken,
  ): Promise<T> {
    const url = `${this.baseUrl}/api/v1${path}`;
    const controller = new AbortController();
    const timer = setTimeout(() => controller.abort(), this.timeoutMs);
    try {
      const res = await fetch(url, {
        method,
        headers: {
          authorization: `token ${token}`,
          'content-type': 'application/json',
          accept: 'application/json',
        },
        body: body === undefined ? undefined : JSON.stringify(body),
        signal: controller.signal,
      });
      const text = await res.text();
      if (!res.ok) {
        throw new ForgejoApiError(res.status, method, path, text);
      }
      return (text ? JSON.parse(text) : undefined) as T;
    } finally {
      clearTimeout(timer);
    }
  }

  async healthz(): Promise<boolean> {
    try {
      const res = await fetch(`${this.baseUrl}/api/healthz`);
      return res.ok;
    } catch {
      return false;
    }
  }

  /** Create a per-learner org. Idempotent: an existing org (422) is treated as success. */
  async ensureOrg(orgName: string): Promise<void> {
    try {
      await this.api('POST', '/orgs', {
        username: orgName,
        visibility: 'private',
      });
    } catch (e) {
      if (
        e instanceof ForgejoApiError &&
        (e.status === 422 || e.status === 409)
      ) {
        // already exists
        return;
      }
      throw e;
    }
  }

  /** Create the learner's project repo in their org. Idempotent on 409/422. */
  async ensureRepo(
    orgName: string,
    repoName: string,
    opts: { autoInit: boolean; defaultBranch: string; description?: string },
  ): Promise<ForgejoRepo> {
    try {
      await this.api('POST', `/orgs/${enc(orgName)}/repos`, {
        name: repoName,
        private: true,
        auto_init: opts.autoInit,
        default_branch: opts.defaultBranch,
        description: opts.description ?? '',
      });
    } catch (e) {
      if (!(
        e instanceof ForgejoApiError &&
        (e.status === 409 || e.status === 422)
      )) {
        throw e;
      }
    }
    return this.getRepo(orgName, repoName);
  }

  async getRepo(orgName: string, repoName: string): Promise<ForgejoRepo> {
    const r = await this.api<{
      full_name: string;
      clone_url: string;
      html_url: string;
      default_branch: string;
      empty: boolean;
    }>('GET', `/repos/${enc(orgName)}/${enc(repoName)}`);
    return {
      fullName: r.full_name,
      cloneUrl: r.clone_url,
      htmlUrl: r.html_url,
      defaultBranch: r.default_branch,
      empty: r.empty,
    };
  }

  /**
   * Create or update a single file on a branch (the seeding primitive).
   * `sha` must be provided to update an existing file; omit to create.
   */
  async putFile(
    orgName: string,
    repoName: string,
    filePath: string,
    contentUtf8: string,
    opts: { branch: string; message: string; sha?: string },
  ): Promise<{ commitSha: string }> {
    const encoded = Buffer.from(contentUtf8, 'utf-8').toString('base64');
    const payload: Record<string, unknown> = {
      content: encoded,
      message: opts.message,
      branch: opts.branch,
    };
    if (opts.sha) payload.sha = opts.sha;
    const res = await this.api<{ commit: { sha: string } }>(
      opts.sha ? 'PUT' : 'POST',
      `/repos/${enc(orgName)}/${enc(repoName)}/contents/${filePath
        .split('/')
        .map(enc)
        .join('/')}`,
      payload,
    );
    return { commitSha: res.commit.sha };
  }

  /** Read a file's UTF-8 content at a ref (branch, tag, or commit sha). */
  async getFileContent(
    orgName: string,
    repoName: string,
    filePath: string,
    ref?: string,
  ): Promise<string> {
    const q = ref ? `?ref=${enc(ref)}` : '';
    const res = await this.api<{ content: string; encoding: string }>(
      'GET',
      `/repos/${enc(orgName)}/${enc(repoName)}/contents/${filePath
        .split('/')
        .map(enc)
        .join('/')}${q}`,
    );
    return res.encoding === 'base64'
      ? Buffer.from(res.content, 'base64').toString('utf-8')
      : res.content;
  }

  /** Commit history, newest first. `sha` can pin a branch/commit; `limit` caps results. */
  async listCommits(
    orgName: string,
    repoName: string,
    opts: { sha?: string; limit?: number; path?: string } = {},
  ): Promise<ForgejoCommit[]> {
    const params = new URLSearchParams();
    if (opts.sha) params.set('sha', opts.sha);
    params.set('limit', String(opts.limit ?? 30));
    if (opts.path) params.set('path', opts.path);
    const rows = await this.api<
      Array<{
        sha: string;
        html_url: string;
        commit: {
          message: string;
          author: { name: string; date: string };
        };
      }>
    >(
      'GET',
      `/repos/${enc(orgName)}/${enc(repoName)}/commits?${params.toString()}`,
    );
    return rows.map((r) => ({
      sha: r.sha,
      message: r.commit.message,
      authorName: r.commit.author?.name ?? '',
      authoredAt: r.commit.author?.date ?? '',
      htmlUrl: r.html_url,
    }));
  }

  /** Raw unified diff for a single commit (`git show`-style). */
  async getCommitDiff(
    orgName: string,
    repoName: string,
    sha: string,
  ): Promise<string> {
    const url = `${this.baseUrl}/api/v1/repos/${enc(orgName)}/${enc(
      repoName,
    )}/git/commits/${enc(sha)}.diff`;
    const controller = new AbortController();
    const timer = setTimeout(() => controller.abort(), this.timeoutMs);
    try {
      const res = await fetch(url, {
        headers: { authorization: `token ${this.adminToken}` },
        signal: controller.signal,
      });
      const text = await res.text();
      if (!res.ok) {
        throw new ForgejoApiError(res.status, 'GET', url, text);
      }
      return text;
    } finally {
      clearTimeout(timer);
    }
  }

  /**
   * Unified diff for base…head, concatenating the per-commit diffs of
   * every commit in the range (oldest → newest). Used by the viva
   * generator (Stage 3.8) to probe design-vs-implementation divergence.
   *
   * Forgejo's `/compare/base...head.diff` endpoint is unreliable across
   * versions (500s on the one this runs against); walking `git log`-style
   * per-commit diffs is version-stable.
   */
  async getDiff(
    orgName: string,
    repoName: string,
    base: string,
    head: string,
  ): Promise<string> {
    // commits reachable from head, newest first; stop when we hit base
    const commits = await this.listCommits(orgName, repoName, {
      sha: head,
      limit: 250,
    });
    const inRange: string[] = [];
    for (const c of commits) {
      if (c.sha === base) break;
      inRange.push(c.sha);
    }
    inRange.reverse(); // oldest → newest
    const parts: string[] = [];
    for (const sha of inRange) {
      parts.push(await this.getCommitDiff(orgName, repoName, sha));
    }
    return parts.join('\n');
  }

  /**
   * Ensure a learner user exists. Always (re)sets the account password to
   * a fresh random value and returns it — Forgejo's token-mint endpoint
   * (`POST /users/{name}/tokens`) only accepts that user's own basic
   * auth, not an admin token, so GitService needs the password to mint
   * the learner's repo-scoped push token. The password is handed to the
   * learner alongside the token and not stored.
   */
  async ensureUser(
    username: string,
    email: string,
  ): Promise<{ username: string; password: string; created: boolean }> {
    const password = cryptoRandom(24);
    try {
      await this.api('POST', '/admin/users', {
        username,
        email,
        password,
        must_change_password: false,
        source_id: 0,
        login_name: username,
      });
      return { username, password, created: true };
    } catch (e) {
      if (
        e instanceof ForgejoApiError &&
        (e.status === 422 || e.status === 409)
      ) {
        // exists — rotate the password so the caller has a working one
        await this.api('PATCH', `/admin/users/${enc(username)}`, {
          login_name: username,
          source_id: 0,
          password,
          must_change_password: false,
        });
        return { username, password, created: false };
      }
      throw e;
    }
  }

  /** Give a user write access to one repo (collaborator with `write`). */
  async addRepoCollaborator(
    orgName: string,
    repoName: string,
    username: string,
  ): Promise<void> {
    await this.api(
      'PUT',
      `/repos/${enc(orgName)}/${enc(repoName)}/collaborators/${enc(username)}`,
      { permission: 'write' },
    );
  }

  /**
   * Mint a repo-scoped access token for a learner (write:repository), used
   * as the HTTP push credential. Returned once — the caller hands it to
   * the learner and does not store it.
   *
   * Forgejo only lets a user create their own tokens, authenticated with
   * their own basic auth — an admin token is rejected here (401). So this
   * takes the learner's password (from ensureUser) and sends
   * `Authorization: Basic`.
   */
  async createUserScopedToken(
    username: string,
    password: string,
    tokenName: string,
  ): Promise<string> {
    const url = `${this.baseUrl}/api/v1/users/${enc(username)}/tokens`;
    const basic = Buffer.from(`${username}:${password}`).toString('base64');
    const controller = new AbortController();
    const timer = setTimeout(() => controller.abort(), this.timeoutMs);
    try {
      const res = await fetch(url, {
        method: 'POST',
        headers: {
          authorization: `Basic ${basic}`,
          'content-type': 'application/json',
          accept: 'application/json',
        },
        body: JSON.stringify({
          name: tokenName,
          scopes: ['write:repository'],
        }),
        signal: controller.signal,
      });
      const text = await res.text();
      if (!res.ok) {
        throw new ForgejoApiError(res.status, 'POST', url, text);
      }
      return (JSON.parse(text) as { sha1: string }).sha1;
    } finally {
      clearTimeout(timer);
    }
  }

  async deleteRepo(orgName: string, repoName: string): Promise<void> {
    await this.api('DELETE', `/repos/${enc(orgName)}/${enc(repoName)}`);
  }

  async deleteOrg(orgName: string): Promise<void> {
    await this.api('DELETE', `/orgs/${enc(orgName)}`);
  }
}

function enc(s: string): string {
  return encodeURIComponent(s);
}

function cryptoRandom(len: number): string {
  return randomBytes(len).toString('base64url').slice(0, len);
}
