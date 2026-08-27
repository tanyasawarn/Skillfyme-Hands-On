import type { Kysely } from 'kysely';
import type { Database } from '../../src/db/schema';
import { AttemptRepository } from '../../src/modules/attempt/attempt.repository';
import { CommandExecutedConsumer } from '../../src/modules/attempt/command-executed.consumer';
import { EventStoreRepository } from '../../src/modules/event-store/event-store.repository';
import { SkillRepository } from '../../src/modules/skill/skill.repository';
import { CatalogRepository } from '../../src/modules/catalog/catalog.repository';
import { ActivitySpecReader } from '../../src/common/activity-spec-reader';
import { createTestDb, truncateAll } from './test-db';

/**
 * PLAN.md Phase 2 integration point: "blast_radius forbidden commands
 * are detected in Dev A's Session Broker tap but scored by Dev B's
 * scoring engine -- same event-based handoff as Phase 1's
 * COMMAND_EXECUTED, no new mechanism needed." Before CommandExecutedConsumer
 * existed, that handoff mechanism didn't actually exist practice-core-side
 * at all -- COMMAND_EXECUTED was published by the orchestrator but never
 * consumed. These tests exercise the consumer's DB-touching logic
 * directly (handleCommand is private; NATS onModuleInit is never called
 * here, matching how GrpcValidatorExecutor's own spec tests its
 * gRPC-adjacent logic without a real connection).
 */
describe('CommandExecutedConsumer (integration, real Postgres)', () => {
  let db: Kysely<Database>;
  let attemptRepo: AttemptRepository;
  let events: EventStoreRepository;
  let catalog: CatalogRepository;
  let consumer: CommandExecutedConsumer;
  let tenantId: string;
  let userId: string;

  beforeAll(() => {
    db = createTestDb();
    attemptRepo = new AttemptRepository(db);
    events = new EventStoreRepository(db);
    catalog = new CatalogRepository(db);
    consumer = new CommandExecutedConsumer(
      db,
      undefined as never,
      events,
      new ActivitySpecReader(db),
    );
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
    tenantId = tenant.id;
    userId = user.id;
    const skillRepo = new SkillRepository(db);
    await skillRepo.createSkill({
      slug: 'k8s.troubleshooting',
      name: 'K8s Troubleshooting',
      domain: 'k8s',
    });
  });

  async function handleCommand(attemptId: string, cmd: string) {
    return (
      consumer as unknown as {
        handleCommand: (
          id: string,
          p: Record<string, unknown>,
        ) => Promise<void>;
      }
    ).handleCommand(attemptId, {
      cmd,
      exit_code: 0,
      duration_ms: 10,
      cmd_hash: 'x',
      source: 'web_terminal',
    });
  }

  async function makeProductionSimAttempt() {
    const version = await catalog.publishNewVersion({
      tenantId,
      activitySlug: 'lab.sim.blast-radius-test',
      mode: 'PRODUCTION_SIM',
      spec: {
        id: 'lab.sim.blast-radius-test',
        version: 1,
        meta: { difficulty_level: 'L3', estimated_minutes: 30 },
        environment: { blueprint: 'bp.test.v1', cost_budget_usd: 0.08 },
        skills: [{ skill: 'k8s.troubleshooting', weight: 1.0, primary: true }],
        process_signals: {
          blast_radius: {
            forbidden: ['kubectl delete ns', 'terraform destroy'],
          },
          diagnostic_efficiency: {
            good_actions: ['kubectl describe pod', 'kubectl logs'],
            bad_actions: ['kubectl delete pod --all'],
          },
        },
      },
    });
    return attemptRepo.create({
      tenantId,
      userId,
      activityId: version.activity_id,
      activityVersionId: version.id,
      mode: 'PRODUCTION_SIM',
    });
  }

  async function signalValue(
    attemptId: string,
    key: string,
  ): Promise<number | null> {
    const row = await db
      .selectFrom('attempt.attempt_signal')
      .select('value_num')
      .where('attempt_id', '=', attemptId)
      .where('signal_key', '=', key)
      .executeTakeFirst();
    return row ? Number(row.value_num) : null;
  }

  it('appends every command to attempt_events as COMMAND_EXECUTED', async () => {
    const attempt = await makeProductionSimAttempt();
    await handleCommand(attempt.id, 'kubectl get pods');

    const rows = await db
      .selectFrom('attempt.attempt_events')
      .select(['type', 'payload'])
      .where('attempt_id', '=', attempt.id)
      .where('type', '=', 'COMMAND_EXECUTED')
      .execute();
    expect(rows).toHaveLength(1);
    expect((rows[0].payload as { cmd: string }).cmd).toBe('kubectl get pods');
  });

  it('increments blast_radius_violations when a command matches process_signals.blast_radius.forbidden', async () => {
    const attempt = await makeProductionSimAttempt();
    await handleCommand(attempt.id, 'kubectl delete ns checkout --force');
    expect(await signalValue(attempt.id, 'blast_radius_violations')).toBe(1);
  });

  it('does not increment blast_radius_violations for a benign command', async () => {
    const attempt = await makeProductionSimAttempt();
    await handleCommand(attempt.id, 'kubectl get pods');
    expect(await signalValue(attempt.id, 'blast_radius_violations')).toBeNull();
  });

  it('accumulates across multiple violating commands', async () => {
    const attempt = await makeProductionSimAttempt();
    await handleCommand(attempt.id, 'kubectl delete ns checkout');
    await handleCommand(attempt.id, 'terraform destroy -auto-approve');
    expect(await signalValue(attempt.id, 'blast_radius_violations')).toBe(2);
  });

  it('increments diagnostic_efficiency good/bad counts independently', async () => {
    const attempt = await makeProductionSimAttempt();
    await handleCommand(attempt.id, 'kubectl describe pod checkout-abc123');
    await handleCommand(attempt.id, 'kubectl logs checkout-abc123');
    await handleCommand(attempt.id, 'kubectl delete pod --all');

    expect(
      await signalValue(attempt.id, 'diagnostic_efficiency_good_count'),
    ).toBe(2);
    expect(
      await signalValue(attempt.id, 'diagnostic_efficiency_bad_count'),
    ).toBe(1);
  });

  it('does nothing (no signal rows) for a GUIDED_LAB attempt with no process_signals authored', async () => {
    const version = await catalog.publishNewVersion({
      tenantId,
      activitySlug: 'lab.guided.no-signals',
      mode: 'GUIDED_LAB',
      spec: {
        id: 'lab.guided.no-signals',
        version: 1,
        meta: { difficulty_level: 'L1', estimated_minutes: 20 },
        environment: { blueprint: 'bp.test.v1', cost_budget_usd: 0.02 },
        skills: [{ skill: 'k8s.troubleshooting', weight: 1.0, primary: true }],
      },
    });
    const attempt = await attemptRepo.create({
      tenantId,
      userId,
      activityId: version.activity_id,
      activityVersionId: version.id,
      mode: 'GUIDED_LAB',
    });

    // Even a command that WOULD match blast_radius wording elsewhere
    // must not increment anything -- this activity authored no
    // process_signals at all, so there's nothing to check against.
    await handleCommand(attempt.id, 'kubectl delete ns checkout');
    expect(await signalValue(attempt.id, 'blast_radius_violations')).toBeNull();

    // The raw event still gets appended regardless -- that half of the
    // consumer's job (doc §4.2: attempt_events is the sink for
    // everything) doesn't depend on process_signals existing.
    const rows = await db
      .selectFrom('attempt.attempt_events')
      .select('type')
      .where('attempt_id', '=', attempt.id)
      .where('type', '=', 'COMMAND_EXECUTED')
      .execute();
    expect(rows).toHaveLength(1);
  });
});
