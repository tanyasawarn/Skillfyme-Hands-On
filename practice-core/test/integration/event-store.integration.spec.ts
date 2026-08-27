import type { Kysely } from 'kysely';
import type { Database } from '../../src/db/schema';
import { EventStoreRepository } from '../../src/modules/event-store/event-store.repository';
import { createTestDb, truncateAll } from './test-db';

describe('EventStoreRepository (integration, real Postgres)', () => {
  let db: Kysely<Database>;
  let events: EventStoreRepository;
  let attemptId: string;

  beforeAll(() => {
    db = createTestDb();
    events = new EventStoreRepository(db);
  });

  afterAll(async () => {
    await db.destroy();
  });

  beforeEach(async () => {
    await truncateAll(db);

    const tenant = await db
      .insertInto('learner.tenant')
      .values({ name: 'test-tenant' })
      .returningAll()
      .executeTakeFirstOrThrow();
    const user = await db
      .insertInto('learner.user_account')
      .values({ tenant_id: tenant.id, email: 'learner@test.dev' })
      .returningAll()
      .executeTakeFirstOrThrow();
    await db
      .insertInto('skill.skill')
      .values({ slug: 'test.skill', name: 'Test', domain: 'test' })
      .execute();
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

  it('assigns seq starting at 1 and incrementing monotonically per attempt', async () => {
    const e1 = await events.append({
      attemptId,
      actor: 'SYSTEM',
      type: 'ATTEMPT_CREATED',
      payload: {},
    });
    const e2 = await events.append({
      attemptId,
      actor: 'LEARNER',
      type: 'ATTEMPT_STARTED',
      payload: {},
    });
    const e3 = await events.append({
      attemptId,
      actor: 'VALIDATOR',
      type: 'TASK_PASSED',
      payload: { task: 't1' },
    });

    expect(e1.seq).toBe('1');
    expect(e2.seq).toBe('2');
    expect(e3.seq).toBe('3');
  });

  it('replay returns events in seq order with correct payload round-tripping', async () => {
    await events.append({
      attemptId,
      actor: 'SYSTEM',
      type: 'ATTEMPT_CREATED',
      payload: { foo: 'bar' },
    });
    await events.append({
      attemptId,
      actor: 'LEARNER',
      type: 'ATTEMPT_STARTED',
      payload: { baz: 42 },
    });

    const replayed = await events.replay(attemptId);
    expect(replayed).toHaveLength(2);
    expect(replayed[0].type).toBe('ATTEMPT_CREATED');
    expect(replayed[0].payload).toEqual({ foo: 'bar' });
    expect(replayed[1].type).toBe('ATTEMPT_STARTED');
    expect(replayed[1].payload).toEqual({ baz: 42 });
  });

  // PLAN.md Phase 3's K8: EventStoreRepository itself stays a generic,
  // untyped-string event log (see event-store.repository.ts's own doc
  // comment for why) -- these ordering/concurrency tests exercise that
  // generic layer directly with arbitrary type strings ('A'/'B'/'C'/
  // `EVT_${n}`), unrelated to any real business event. Real callers get
  // compile-time taxonomy checking via TypedAppendEventInput /
  // appendTypedEvent() instead (attempt-event-type.ts), not by
  // constraining this repository's own generic `type: string`.
  it('listSince returns only events after the given seq', async () => {
    await events.append({ attemptId, actor: 'SYSTEM', type: 'A', payload: {} });
    const second = await events.append({
      attemptId,
      actor: 'SYSTEM',
      type: 'B',
      payload: {},
    });
    await events.append({ attemptId, actor: 'SYSTEM', type: 'C', payload: {} });

    const since = await events.listSince(attemptId, second.seq);
    expect(since).toHaveLength(1);
    expect(since[0].type).toBe('C');
  });

  it('assigns strictly monotonic, gap-free seq under concurrent appends to the same attempt', async () => {
    // This is the scenario the advisory lock exists for: doc §4.2 notes
    // both the Session Broker (Dev A) and Practice Core can be producing
    // events for the same attempt concurrently. Fire 20 concurrent
    // appends and verify no duplicate/skipped seq numbers result.
    const concurrency = 20;
    const results = await Promise.all(
      Array.from({ length: concurrency }, (_, i) =>
        events.append({
          attemptId,
          actor: 'SYSTEM',
          type: `EVT_${i}`,
          payload: { i },
        }),
      ),
    );

    const seqNumbers = results.map((r) => Number(r.seq)).sort((a, b) => a - b);
    const expected = Array.from({ length: concurrency }, (_, i) => i + 1);
    expect(seqNumbers).toEqual(expected);

    const replayed = await events.replay(attemptId);
    expect(replayed).toHaveLength(concurrency);
    // seq column itself must be strictly increasing with no gaps in storage order too.
    const storedSeqs = replayed.map((r) => Number(r.seq));
    expect(storedSeqs).toEqual(expected);
  }, 20_000);

  it('keeps seq counters independent across different attempts', async () => {
    const tenant = await db
      .selectFrom('learner.tenant')
      .selectAll()
      .executeTakeFirstOrThrow();
    const user = await db
      .selectFrom('learner.user_account')
      .selectAll()
      .executeTakeFirstOrThrow();
    const activity = await db
      .selectFrom('content.activity')
      .selectAll()
      .executeTakeFirstOrThrow();
    const version = await db
      .selectFrom('content.activity_version')
      .selectAll()
      .executeTakeFirstOrThrow();
    const attempt2 = await db
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

    await events.append({ attemptId, actor: 'SYSTEM', type: 'A', payload: {} });
    await events.append({ attemptId, actor: 'SYSTEM', type: 'B', payload: {} });
    const otherFirst = await events.append({
      attemptId: attempt2.id,
      actor: 'SYSTEM',
      type: 'X',
      payload: {},
    });

    expect(otherFirst.seq).toBe('1'); // independent counter, not global
  });
});
