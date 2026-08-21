import type { Kysely } from 'kysely';
import type { Database } from '../../src/db/schema';
import { EventStoreRepository } from '../../src/modules/event-store/event-store.repository';
import { ReplayService } from '../../src/modules/event-store/replay.service';
import { createTestDb, truncateAll } from './test-db';

describe('ReplayService (integration, real Postgres) — doc §4.4 recovery mechanism', () => {
  let db: Kysely<Database>;
  let events: EventStoreRepository;
  let replay: ReplayService;
  let attemptId: string;

  beforeAll(() => {
    db = createTestDb();
    events = new EventStoreRepository(db);
    replay = new ReplayService(db, events);
  });

  afterAll(async () => {
    await db.destroy();
  });

  beforeEach(async () => {
    await truncateAll(db);

    const tenant = await db
      .insertInto('learner.tenant')
      .values({ name: 't' })
      .returningAll()
      .executeTakeFirstOrThrow();
    const user = await db
      .insertInto('learner.user_account')
      .values({ tenant_id: tenant.id, email: 'l@test.dev' })
      .returningAll()
      .executeTakeFirstOrThrow();
    const activity = await db
      .insertInto('content.activity')
      .values({ tenant_id: tenant.id, slug: 'lab.test', mode: 'GUIDED_LAB' })
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
    const attempt = await db
      .insertInto('attempt.attempt')
      .values({
        tenant_id: tenant.id,
        user_id: user.id,
        activity_id: activity.id,
        activity_version_id: version.id,
        mode: 'GUIDED_LAB',
      })
      .returningAll()
      .executeTakeFirstOrThrow();
    attemptId = attempt.id;
  });

  it('rebuilds attempt_task_state from a realistic event sequence matching the doc §12.1 sequence diagram', async () => {
    await events.append({
      attemptId,
      actor: 'LEARNER',
      type: 'HINT_REQUESTED',
      payload: { task: 't1', level: 1 },
    });
    await events.append({
      attemptId,
      actor: 'VALIDATOR',
      type: 'TASK_FAILED',
      payload: { task: 't1' },
    });
    await events.append({
      attemptId,
      actor: 'VALIDATOR',
      type: 'TASK_PASSED',
      payload: { task: 't1' },
    });
    await events.append({
      attemptId,
      actor: 'VALIDATOR',
      type: 'TASK_PASSED',
      payload: { task: 't2' },
    });
    await events.append({
      attemptId,
      actor: 'LEARNER',
      type: 'TASK_SKIPPED',
      payload: { task: 't3' },
    });

    const taskCount = await replay.rebuildForAttempt(attemptId);
    expect(taskCount).toBe(3);

    const rows = await db
      .selectFrom('attempt.attempt_task_state')
      .selectAll()
      .where('attempt_id', '=', attemptId)
      .orderBy('task_key')
      .execute();

    expect(rows).toHaveLength(3);

    const t1 = rows.find((r) => r.task_key === 't1')!;
    expect(t1.status).toBe('PASSED'); // failed then passed -> sticky pass
    expect(t1.attempts_count).toBe(2); // one fail + one pass
    expect(t1.hints_used_max_level).toBe(1);
    expect(t1.skipped).toBe(false);

    const t2 = rows.find((r) => r.task_key === 't2')!;
    expect(t2.status).toBe('PASSED');
    expect(t2.attempts_count).toBe(1);

    const t3 = rows.find((r) => r.task_key === 't3')!;
    expect(t3.status).toBe('SKIPPED');
    expect(t3.skipped).toBe(true);
    expect(t3.assisted).toBe(true);
  });

  it('is idempotent and recovers identical state after materialised state is corrupted/deleted (the actual recovery scenario)', async () => {
    await events.append({
      attemptId,
      actor: 'VALIDATOR',
      type: 'TASK_PASSED',
      payload: { task: 't1' },
    });
    await events.append({
      actor: 'VALIDATOR',
      attemptId,
      type: 'TASK_PASSED',
      payload: { task: 't2' },
    });

    await replay.rebuildForAttempt(attemptId);
    const before = await db
      .selectFrom('attempt.attempt_task_state')
      .selectAll()
      .where('attempt_id', '=', attemptId)
      .orderBy('task_key')
      .execute();

    // Simulate the data-bug scenario the doc is defending against: someone
    // (a bad migration, a bug, an admin fat-finger) wipes the materialised
    // view table. attempt_events, the source of truth, is untouched.
    await db
      .deleteFrom('attempt.attempt_task_state')
      .where('attempt_id', '=', attemptId)
      .execute();
    const wiped = await db
      .selectFrom('attempt.attempt_task_state')
      .selectAll()
      .where('attempt_id', '=', attemptId)
      .execute();
    expect(wiped).toHaveLength(0);

    await replay.rebuildForAttempt(attemptId);
    const after = await db
      .selectFrom('attempt.attempt_task_state')
      .selectAll()
      .where('attempt_id', '=', attemptId)
      .orderBy('task_key')
      .execute();

    expect(after).toEqual(before);
  });

  it('never demotes a PASSED task back to FAILED on a later failed re-attempt', async () => {
    await events.append({
      attemptId,
      actor: 'VALIDATOR',
      type: 'TASK_PASSED',
      payload: { task: 't1' },
    });
    await events.append({
      attemptId,
      actor: 'VALIDATOR',
      type: 'TASK_FAILED',
      payload: { task: 't1' },
    });

    await replay.rebuildForAttempt(attemptId);
    const row = await db
      .selectFrom('attempt.attempt_task_state')
      .selectAll()
      .where('attempt_id', '=', attemptId)
      .where('task_key', '=', 't1')
      .executeTakeFirstOrThrow();

    expect(row.status).toBe('PASSED');
    expect(row.attempts_count).toBe(2);
  });
});
