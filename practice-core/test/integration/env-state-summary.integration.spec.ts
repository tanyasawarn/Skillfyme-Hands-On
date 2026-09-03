import type { Kysely } from 'kysely';
import type { Database } from '../../src/db/schema';
import { EnvStateSummaryService } from '../../src/modules/mentor/env-state-summary.service';
import { EventStoreRepository } from '../../src/modules/event-store/event-store.repository';
import { createTestDb, truncateAll } from './test-db';

/**
 * PLAN.md G2 / doc §7.4 -- the read-only structured env-state summary the
 * Mentor Service consumes. This seeds an attempt's COMMAND_EXECUTED /
 * FILE_CHANGED events + validator results + task state and asserts the
 * summary assembles them correctly, attempt-scoped, with no solution
 * content anywhere (there is no code path to one).
 */
describe('EnvStateSummaryService (§7.4, G2)', () => {
  let db: Kysely<Database>;
  let svc: EnvStateSummaryService;
  let events: EventStoreRepository;

  let tenantId: string;
  let userId: string;
  let attemptId: string;
  let otherAttemptId: string;

  beforeAll(() => {
    db = createTestDb();
    svc = new EnvStateSummaryService(db);
    events = new EventStoreRepository(db);
  });

  afterAll(async () => {
    await db.destroy();
  });

  beforeEach(async () => {
    await truncateAll(db);
    const tenant = await db
      .insertInto('learner.tenant')
      .values({ name: 'ess-tenant' })
      .returningAll()
      .executeTakeFirstOrThrow();
    tenantId = tenant.id;
    const user = await db
      .insertInto('learner.user_account')
      .values({ tenant_id: tenantId, email: 'ess@test.dev' })
      .returningAll()
      .executeTakeFirstOrThrow();
    userId = user.id;

    await db
      .insertInto('skill.skill')
      .values({ slug: 'ess.skill', name: 'ESS', domain: 'test' })
      .execute();
    const activity = await db
      .insertInto('content.activity')
      .values({ tenant_id: tenantId, slug: 'lab.ess', mode: 'GUIDED_LAB' })
      .returningAll()
      .executeTakeFirstOrThrow();
    const version = await db
      .insertInto('content.activity_version')
      .values({
        activity_id: activity.id,
        version: 1,
        status: 'PUBLISHED',
        spec_jsonb: {},
      })
      .returningAll()
      .executeTakeFirstOrThrow();

    const mk = async () =>
      (
        await db
          .insertInto('attempt.attempt')
          .values({
            tenant_id: tenantId,
            user_id: userId,
            activity_id: activity.id,
            activity_version_id: version.id,
            mode: 'GUIDED_LAB',
          })
          .returningAll()
          .executeTakeFirstOrThrow()
      ).id;
    attemptId = await mk();
    otherAttemptId = await mk();

    // commands on THIS attempt
    await events.append({
      attemptId,
      actor: 'LEARNER',
      type: 'COMMAND_EXECUTED',
      payload: { cmd: 'kubectl get pods', exit_code: 0, duration_ms: 120 },
    });
    await events.append({
      attemptId,
      actor: 'LEARNER',
      type: 'COMMAND_EXECUTED',
      payload: {
        cmd: 'kubectl describe pod checkout-xyz',
        exit_code: 1,
        duration_ms: 80,
        stderr: 'Error from server (NotFound): pods "checkout-xyz" not found',
      },
    });
    await events.append({
      attemptId,
      actor: 'LEARNER',
      type: 'FILE_CHANGED',
      payload: { path: '/workspace/deploy.yaml', op: 'write' },
    });

    // a command on the OTHER attempt -- must not bleed in
    await events.append({
      attemptId: otherAttemptId,
      actor: 'LEARNER',
      type: 'COMMAND_EXECUTED',
      payload: { cmd: 'echo other-attempt-secret', exit_code: 0 },
    });

    // validator results for THIS attempt
    const run = await db
      .insertInto('attempt.validation_run')
      .values({ attempt_id: attemptId, scope: 'all', trigger: 'submit' })
      .returningAll()
      .executeTakeFirstOrThrow();
    await db
      .insertInto('attempt.validator_result')
      .values([
        {
          validation_run_id: run.id,
          validator_id: 'v.image-built',
          status: 'PASS',
          observed: { exit_code: 0 },
          expected: { exit_code: 0 },
        },
        {
          validation_run_id: run.id,
          validator_id: 'v.probe-configured',
          status: 'FAIL',
          observed: { path: '/healthz-wrong' },
          expected: { path: '/healthz' },
        },
      ])
      .execute();

    await db
      .insertInto('attempt.attempt_task_state')
      .values([
        { attempt_id: attemptId, task_key: 't1', status: 'PASSED' },
        {
          attempt_id: attemptId,
          task_key: 't2',
          status: 'FAILED',
          assisted: true,
          hints_used_max_level: 2,
        },
      ])
      .execute();
  });

  it('assembles tasks, validators, commands, stderr, and changed files', async () => {
    const s = await svc.summarize(attemptId);

    expect(s.tasks.map((t) => t.taskKey)).toEqual(['t1', 't2']);
    expect(s.tasks.find((t) => t.taskKey === 't2')?.assisted).toBe(true);

    expect(s.validators.map((v) => v.validatorId).sort()).toEqual([
      'v.image-built',
      'v.probe-configured',
    ]);
    const failing = s.validators.find((v) => v.status === 'FAIL');
    expect(failing?.observed).toEqual({ path: '/healthz-wrong' });
    expect(failing?.expected).toEqual({ path: '/healthz' });

    expect(s.recentCommands.map((c) => c.cmd)).toEqual([
      'kubectl get pods',
      'kubectl describe pod checkout-xyz',
    ]);
    expect(s.recentCommands[1].exitCode).toBe(1);

    expect(s.stderrExcerpts[0]).toContain('NotFound');
    expect(s.resourceSummary.changedFiles).toContain('/workspace/deploy.yaml');
  });

  it("is strictly attempt-scoped — never returns another attempt's data", async () => {
    const s = await svc.summarize(attemptId);
    const all = JSON.stringify(s);
    expect(all).not.toContain('other-attempt-secret');
  });

  it('respects the recentCommands cap', async () => {
    for (let i = 0; i < 40; i++) {
      await events.append({
        attemptId,
        actor: 'LEARNER',
        type: 'COMMAND_EXECUTED',
        payload: { cmd: `echo n${i}`, exit_code: 0 },
      });
    }
    const s = await svc.summarize(attemptId, { recentCommands: 10 });
    expect(s.recentCommands).toHaveLength(10);
    // newest kept: n39 must be present, n0 must not
    expect(s.recentCommands.some((c) => c.cmd === 'echo n39')).toBe(true);
    expect(s.recentCommands.some((c) => c.cmd === 'echo n0')).toBe(false);
  });

  it('returns empty structures for an attempt with no activity yet', async () => {
    const s = await svc.summarize(otherAttemptId);
    // other attempt has exactly one command and nothing else
    expect(s.tasks).toEqual([]);
    expect(s.validators).toEqual([]);
    expect(s.recentCommands).toHaveLength(1);
  });
});
