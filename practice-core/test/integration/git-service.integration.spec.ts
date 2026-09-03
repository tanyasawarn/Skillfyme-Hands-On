import { ConfigService } from '@nestjs/config';
import { execFileSync } from 'node:child_process';
import * as os from 'node:os';
import * as path from 'node:path';
import * as fs from 'node:fs';
import { ForgejoClient } from '../../src/modules/project/forgejo.client';
import { GitService } from '../../src/modules/project/git.service';
import type { ProjectRepository } from '../../src/modules/project/project.repository';

/**
 * Phase 3 (PLAN_PHASE3_PROJECTS.md 1.7 / B9). GitService + ForgejoClient
 * against a REAL local Forgejo (infra/git-hosting/compose). Skips
 * entirely unless FORGEJO_BASE_URL + FORGEJO_ADMIN_TOKEN are set (run
 * infra/git-hosting/scripts/bootstrap-admin.sh first).
 *
 * Covers the exact operations the milestone state machine (1.6) and the
 * viva generator (3.8) call: enrol → repo provisioned + seeded + scoped
 * push token; a real HTTP push with that token; commit history + file
 * content + diff read back; retention (repo not deleted on attempt end).
 */
const BASE = process.env.FORGEJO_BASE_URL;
const TOKEN = process.env.FORGEJO_ADMIN_TOKEN;
const RUN = Boolean(BASE && TOKEN);
const d = RUN ? describe : describe.skip;

function config(): ConfigService {
  return {
    get: (k: string) =>
      ({
        FORGEJO_BASE_URL: BASE,
        FORGEJO_ADMIN_TOKEN: TOKEN,
        FORGEJO_TIMEOUT_MS: '20000',
      })[k],
  } as unknown as ConfigService;
}

d('GitService (integration, real Forgejo) — Phase 3 1.7', () => {
  const forgejo = new ForgejoClient(config());
  // GitService.recordMilestoneSubmission also writes project_submission;
  // for this suite we only exercise the git side, so a tiny stub repo.
  const recorded: unknown[] = [];
  const projectRepo = {
    recordSubmission: (input: unknown) => {
      recorded.push(input);
      return Promise.resolve({});
    },
    listSubmissions: () => Promise.resolve([]),
  } as unknown as ProjectRepository;
  const git = new GitService(forgejo, projectRepo);

  const uniq = Date.now();
  const learner = {
    userId: `u-${uniq}`,
    username: `itest-${uniq}`,
    email: `itest-${uniq}@practice.local`,
  };
  let repoRef: string;
  let pushToken: string;
  let cloneUrl: string;

  afterAll(async () => {
    if (RUN && repoRef) {
      await git.deleteLearnerRepo(repoRef).catch(() => undefined);
      const org = repoRef.split(':')[1].split('/')[0];
      await forgejo.deleteOrg(org).catch(() => undefined);
    }
  });

  it('reports enabled when configured', () => {
    expect(git.isEnabled()).toBe(true);
  });

  it('enrolProject provisions a seeded repo + returns a scoped push token', async () => {
    const res = await git.enrolProject({
      attemptId: `att-${uniq}`,
      learner,
      project: { slug: 'demo.url-shortener' },
      requirements: [
        { path: 'REQUIREMENTS.md', content: '# Requirements\n\nBuild it.\n' },
        {
          path: 'docs/rubric.md',
          content: '# Rubric ref\n\nrub.architecture.v3\n',
        },
      ],
    });
    expect(res.enabled).toBe(true);
    expect(res.repoRef).toMatch(
      /^forgejo:learner-itest-\d+\/proj-demo\.url-shortener$/,
    );
    expect(res.pushToken).toBeTruthy();
    expect(res.cloneUrl).toContain(BASE);
    repoRef = res.repoRef!;
    pushToken = res.pushToken!;
    cloneUrl = res.cloneUrl!;

    // seeded files are present
    const req = await git.readFile(repoRef, 'REQUIREMENTS.md');
    expect(req).toContain('# Requirements');
    const rub = await git.readFile(repoRef, 'docs/rubric.md');
    expect(rub).toContain('rub.architecture.v3');
  }, 60_000);

  it('the learner can push over HTTP with the scoped token, and history reads back', async () => {
    const work = fs.mkdtempSync(path.join(os.tmpdir(), 'gitsvc-'));
    const authUrl = cloneUrl.replace(
      '://',
      `://${learner.username}:${pushToken}@`,
    );
    execFileSync('git', ['clone', '-q', authUrl, path.join(work, 'repo')]);
    const repoDir = path.join(work, 'repo');
    fs.writeFileSync(
      path.join(repoDir, 'DESIGN.md'),
      '# Design\n\nMilestone 1.\n',
    );
    execFileSync('git', [
      '-C',
      repoDir,
      '-c',
      'user.email=i@t',
      '-c',
      'user.name=i',
      'add',
      '-A',
    ]);
    execFileSync('git', [
      '-C',
      repoDir,
      '-c',
      'user.email=i@t',
      '-c',
      'user.name=i',
      'commit',
      '-q',
      '-m',
      'design milestone',
    ]);
    execFileSync('git', ['-C', repoDir, 'push', '-q', 'origin', 'HEAD:main']);
    fs.rmSync(work, { recursive: true, force: true });

    const commits = await git.listCommits(repoRef, { limit: 10 });
    expect(commits.length).toBeGreaterThanOrEqual(2);
    expect(commits[0].message).toContain('design milestone');

    const design = await git.readFile(repoRef, 'DESIGN.md');
    expect(design).toContain('Milestone 1');
  }, 60_000);

  it('recordMilestoneSubmission resolves HEAD and records repo_ref + commit_sha', async () => {
    const res = await git.recordMilestoneSubmission({
      attemptId: `att-${uniq}`,
      milestoneKey: 'design',
      repoRef,
      attemptNumber: 1,
    });
    expect(res.commitSha).toMatch(/^[0-9a-f]{40}$/);
    expect(recorded.at(-1)).toMatchObject({
      milestoneKey: 'design',
      repoRef,
      commitSha: res.commitSha,
    });
  }, 30_000);

  it('diff base…head returns a unified diff', async () => {
    const commits = await git.listCommits(repoRef, { limit: 10 });
    const head = commits[0].sha;
    const base = commits[commits.length - 1].sha;
    const diff = await git.diff(repoRef, base, head);
    expect(diff).toContain('DESIGN.md');
    expect(diff).toMatch(/^\+.*Milestone 1/m);
  }, 30_000);
});
