import type { Kysely } from 'kysely';
import type { Database } from '../../src/db/schema';
import { PrivacyService } from '../../src/modules/privacy/privacy.service';
import { EventStoreRepository } from '../../src/modules/event-store/event-store.repository';
import { createTestDb, truncateAll } from './test-db';

/**
 * PLAN.md C16 / memory.md §883 -- learner-facing GDPR export + erasure.
 *
 * export(): returns the full archive (attempts + event log + scores +
 *   artifacts + mastery).
 * erase(): anonymises the account, redacts event payloads + artifact
 *   bodies, stamps erased_at, and RETAINS aggregate counters
 *   (learner_activity_state, attempt rows, attempt_score). Idempotent.
 */
describe('PrivacyService — GDPR export + erasure (§883)', () => {
  let db: Kysely<Database>;
  let privacy: PrivacyService;
  let events: EventStoreRepository;

  let tenantId: string;
  let userId: string;
  let otherUserId: string;
  let attemptId: string;
  let activityId: string;

  beforeAll(() => {
    db = createTestDb();
    privacy = new PrivacyService(db);
    events = new EventStoreRepository(db);
  });

  afterAll(async () => {
    await db.destroy();
  });

  beforeEach(async () => {
    await truncateAll(db);

    const tenant = await db
      .insertInto('learner.tenant')
      .values({ name: 'gdpr-tenant' })
      .returningAll()
      .executeTakeFirstOrThrow();
    tenantId = tenant.id;

    const user = await db
      .insertInto('learner.user_account')
      .values({ tenant_id: tenantId, email: 'subject@test.dev' })
      .returningAll()
      .executeTakeFirstOrThrow();
    userId = user.id;

    const other = await db
      .insertInto('learner.user_account')
      .values({ tenant_id: tenantId, email: 'bystander@test.dev' })
      .returningAll()
      .executeTakeFirstOrThrow();
    otherUserId = other.id;

    await db
      .insertInto('skill.skill')
      .values({ slug: 'gdpr.skill', name: 'GDPR Skill', domain: 'test' })
      .execute();

    const activity = await db
      .insertInto('content.activity')
      .values({ tenant_id: tenantId, slug: 'lab.gdpr', mode: 'GUIDED_LAB' })
      .returningAll()
      .executeTakeFirstOrThrow();
    activityId = activity.id;

    const version = await db
      .insertInto('content.activity_version')
      .values({
        activity_id: activityId,
        version: 1,
        status: 'PUBLISHED',
        spec_jsonb: {},
      })
      .returningAll()
      .executeTakeFirstOrThrow();

    const attempt = await db
      .insertInto('attempt.attempt')
      .values({
        tenant_id: tenantId,
        user_id: userId,
        activity_id: activityId,
        activity_version_id: version.id,
        mode: 'GUIDED_LAB',
        status: 'PASSED',
      })
      .returningAll()
      .executeTakeFirstOrThrow();
    attemptId = attempt.id;

    // An event carrying learner-authored content, and an inline artifact.
    await events.append({
      attemptId,
      actor: 'LEARNER',
      type: 'COMMAND_EXECUTED',
      payload: { cmd: 'kubectl get pods -n my-secret-namespace', exit_code: 0 },
    });
    await db
      .insertInto('attempt.artifact')
      .values({
        attempt_id: attemptId,
        kind: 'incident_note',
        storage_uri: 'local://inline',
        content:
          'Root cause: I misconfigured the readiness probe. Personal note here.',
      })
      .execute();

    // Aggregate counter that must SURVIVE erasure.
    await db
      .insertInto('learner.learner_activity_state')
      .values({
        user_id: userId,
        activity_id: activityId,
        status: 'passed',
        best_score: 0.82,
      })
      .execute();

    // A bystander's attempt + event that must NOT be touched.
    const otherAttempt = await db
      .insertInto('attempt.attempt')
      .values({
        tenant_id: tenantId,
        user_id: otherUserId,
        activity_id: activityId,
        activity_version_id: version.id,
        mode: 'GUIDED_LAB',
      })
      .returningAll()
      .executeTakeFirstOrThrow();
    await events.append({
      attemptId: otherAttempt.id,
      actor: 'LEARNER',
      type: 'COMMAND_EXECUTED',
      payload: { cmd: 'ls', exit_code: 0 },
    });
  });

  it('export returns the full archive for the subject', async () => {
    const archive = await privacy.exportForUser(userId);

    expect(archive.attempts).toHaveLength(1);
    expect((archive.attempts[0] as { id: string }).id).toBe(attemptId);
    expect(archive.attempt_events).toHaveLength(1);
    expect(
      (archive.attempt_events[0] as { payload: { cmd: string } }).payload.cmd,
    ).toContain('my-secret-namespace');
    expect(archive.artifacts).toHaveLength(1);
    expect((archive.artifacts[0] as { content: string }).content).toContain(
      'readiness probe',
    );
    expect(archive.learner_activity_state).toHaveLength(1);
    expect(typeof archive.generated_at).toBe('string');
  });

  it('export throws for an unknown user', async () => {
    await expect(
      privacy.exportForUser('00000000-0000-0000-0000-000000000000'),
    ).rejects.toThrow(/not found/);
  });

  it('erase anonymises the account and redacts PII, keeping aggregate counters', async () => {
    const result = await privacy.eraseForUser(userId);

    expect(result.alreadyErased).toBe(false);
    expect(result.attemptsAnonymised).toBe(1);
    expect(result.eventPayloadsRedacted).toBe(1);
    expect(result.artifactsRedacted).toBe(1);

    // account anonymised
    const user = await db
      .selectFrom('learner.user_account')
      .select(['email', 'status', 'erased_at'])
      .where('id', '=', userId)
      .executeTakeFirstOrThrow();
    expect(user.email).toBe(`erased-${userId.slice(0, 8)}@deleted.invalid`);
    expect(user.status).toBe('erased');
    expect(user.erased_at).not.toBeNull();

    // event payload redacted, envelope kept
    const ev = await db
      .selectFrom('attempt.attempt_events')
      .select(['type', 'payload'])
      .where('attempt_id', '=', attemptId)
      .executeTakeFirstOrThrow();
    expect(ev.type).toBe('COMMAND_EXECUTED');
    expect(ev.payload).toEqual({});

    // artifact body tombstoned
    const art = await db
      .selectFrom('attempt.artifact')
      .select(['content', 'storage_uri'])
      .where('attempt_id', '=', attemptId)
      .executeTakeFirstOrThrow();
    expect(art.content).toBeNull();
    expect(art.storage_uri).toBe('erased://');

    // attempt row retained, marked
    const attempt = await db
      .selectFrom('attempt.attempt')
      .select(['id', 'erased_at', 'status'])
      .where('id', '=', attemptId)
      .executeTakeFirstOrThrow();
    expect(attempt.erased_at).not.toBeNull();
    expect(attempt.status).toBe('PASSED');

    // AGGREGATE COUNTER SURVIVES
    const las = await db
      .selectFrom('learner.learner_activity_state')
      .select(['status', 'best_score'])
      .where('user_id', '=', userId)
      .executeTakeFirstOrThrow();
    expect(las.status).toBe('passed');
    expect(Number(las.best_score)).toBeCloseTo(0.82);
  });

  it("erase does not touch another learner's data", async () => {
    await privacy.eraseForUser(userId);

    const other = await db
      .selectFrom('learner.user_account')
      .select(['email', 'erased_at'])
      .where('id', '=', otherUserId)
      .executeTakeFirstOrThrow();
    expect(other.email).toBe('bystander@test.dev');
    expect(other.erased_at).toBeNull();

    const otherEv = await db
      .selectFrom('attempt.attempt_events')
      .innerJoin(
        'attempt.attempt',
        'attempt.attempt.id',
        'attempt.attempt_events.attempt_id',
      )
      .select(['attempt.attempt_events.payload'])
      .where('attempt.attempt.user_id', '=', otherUserId)
      .executeTakeFirstOrThrow();
    expect(otherEv.payload).toEqual({ cmd: 'ls', exit_code: 0 });
  });

  it('erase is idempotent — a second call is a no-op', async () => {
    await privacy.eraseForUser(userId);
    const second = await privacy.eraseForUser(userId);

    expect(second.alreadyErased).toBe(true);
    expect(second.attemptsAnonymised).toBe(0);
    expect(second.eventPayloadsRedacted).toBe(0);
  });
});
