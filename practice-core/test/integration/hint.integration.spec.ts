import type { Kysely } from 'kysely';
import type { Database } from '../../src/db/schema';
import { AttemptRepository } from '../../src/modules/attempt/attempt.repository';
import { AttemptService } from '../../src/modules/attempt/attempt.service';
import { EligibilityService } from '../../src/modules/attempt/eligibility.service';
import { HintService } from '../../src/modules/attempt/hint.service';
import { EventStoreRepository } from '../../src/modules/event-store/event-store.repository';
import { FakeOrchestratorClient } from '../../src/modules/attempt/fake-orchestrator.client';
import { SkillRepository } from '../../src/modules/skill/skill.repository';
import { MasteryService } from '../../src/modules/skill/mastery.service';
import { BktService } from '../../src/modules/skill/bkt.service';
import { CatalogRepository } from '../../src/modules/catalog/catalog.repository';
import { EvaluationService } from '../../src/modules/evaluation/evaluation.service';
import { ValidatorRunnerService } from '../../src/modules/evaluation/validator-runner.service';
import { ScoringEngineService } from '../../src/modules/evaluation/scoring-engine.service';
import { FakeValidatorExecutor } from '../../src/modules/evaluation/fake-validator-executor';
import { ReplayService } from '../../src/modules/event-store/replay.service';
import { createTestDb, truncateAll } from './test-db';

describe('HintService (integration, real Postgres) — doc §7.5 static hint ladder', () => {
  let db: Kysely<Database>;
  let attemptService: AttemptService;
  let hints: HintService;
  let catalog: CatalogRepository;
  let events: EventStoreRepository;
  let tenantId: string;
  let userId: string;

  beforeAll(() => {
    db = createTestDb();
    const attemptRepo = new AttemptRepository(db);
    const skillRepo = new SkillRepository(db);
    const mastery = new MasteryService(db, new BktService());
    const eligibility = new EligibilityService(db, skillRepo, mastery);
    events = new EventStoreRepository(db);
    const orchestrator = new FakeOrchestratorClient(5);
    const validatorRunner = new ValidatorRunnerService(
      db,
      events,
      new FakeValidatorExecutor(),
    );
    const replay = new ReplayService(db, events);
    const evaluation = new EvaluationService(
      db,
      events,
      validatorRunner,
      new ScoringEngineService(),
      mastery,
      replay,
    );
    attemptService = new AttemptService(
      db,
      attemptRepo,
      eligibility,
      events,
      orchestrator,
      evaluation,
    );
    hints = new HintService(db, events);
    catalog = new CatalogRepository(db);
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
    await db
      .insertInto('skill.skill')
      .values({ slug: 'k8s.core', name: 'K8s Core', domain: 'k8s' })
      .execute();
  });

  async function publishWithHints() {
    return catalog.publishNewVersion({
      tenantId,
      activitySlug: 'lab.hint-test',
      mode: 'GUIDED_LAB',
      spec: {
        id: 'lab.hint-test',
        version: 1,
        meta: { difficulty_level: 'L2', estimated_minutes: 35 },
        environment: { blueprint: 'bp.test.v1', cost_budget_usd: 0.05 },
        skills: [{ skill: 'k8s.core', weight: 1.0, primary: true }],
        tasks: [
          {
            key: 't1',
            required: true,
            validators: [
              {
                id: 'v1',
                type: 'SHELL_ASSERT',
                expect: { exit_code: 0 },
                weight: 1.0,
                severity: 'BLOCKING',
              },
            ],
            hints: [
              { level: 1, penalty: 0.02, text: 'Nudge: check the Dockerfile.' },
              { level: 2, penalty: 0.05, text: 'Conceptual: tags matter.' },
              {
                level: 3,
                penalty: 0.12,
                text: 'Directive: run docker build -t node-app:v1 .',
              },
            ],
          },
        ],
      } as never,
    });
  }

  async function createAndStartAttempt(versionId: string) {
    const attempt = await attemptService.createAttempt({
      tenantId,
      userId,
      activityVersionId: versionId,
    });
    await attemptService.provision(attempt.id);
    await attemptService.markStarted(attempt.id);
    return attempt.id;
  }

  it('preview returns the next hint level and penalty without any side effect', async () => {
    const version = await publishWithHints();
    const attemptId = await createAndStartAttempt(version.id);

    const preview = await hints.preview(attemptId, 't1');
    expect(preview).toEqual({
      taskKey: 't1',
      nextLevel: 1,
      penalty: 0.02,
      hasMoreAfterThis: true,
    });

    // No side effects: attempt.hint_penalty_total unchanged, no HINT_REQUESTED event.
    const attempt = await db
      .selectFrom('attempt.attempt')
      .selectAll()
      .where('id', '=', attemptId)
      .executeTakeFirstOrThrow();
    expect(Number(attempt.hint_penalty_total)).toBe(0);
    const eventLog = await events.replay(attemptId);
    expect(eventLog.some((e) => e.type === 'HINT_REQUESTED')).toBe(false);
  });

  it('reveal returns the hint text, appends HINT_REQUESTED, and accrues the penalty', async () => {
    const version = await publishWithHints();
    const attemptId = await createAndStartAttempt(version.id);

    const revealed = await hints.reveal(attemptId, 't1');
    expect(revealed.nextLevel).toBe(1);
    expect(revealed.text).toBe('Nudge: check the Dockerfile.');
    expect(revealed.penalty).toBe(0.02);

    const attempt = await db
      .selectFrom('attempt.attempt')
      .selectAll()
      .where('id', '=', attemptId)
      .executeTakeFirstOrThrow();
    expect(Number(attempt.hint_penalty_total)).toBeCloseTo(0.02, 5);

    const eventLog = await events.replay(attemptId);
    const hintEvent = eventLog.find((e) => e.type === 'HINT_REQUESTED');
    expect(hintEvent).toBeDefined();
    expect(hintEvent!.payload).toEqual({ task: 't1', level: 1 });

    const taskState = await db
      .selectFrom('attempt.attempt_task_state')
      .selectAll()
      .where('attempt_id', '=', attemptId)
      .where('task_key', '=', 't1')
      .executeTakeFirstOrThrow();
    expect(taskState.hints_used_max_level).toBe(1);
  });

  it('escalates to the next level on repeated reveals, accruing penalties cumulatively', async () => {
    const version = await publishWithHints();
    const attemptId = await createAndStartAttempt(version.id);

    await hints.reveal(attemptId, 't1'); // level 1, +0.02
    const second = await hints.reveal(attemptId, 't1'); // level 2, +0.05
    expect(second.nextLevel).toBe(2);
    expect(second.text).toBe('Conceptual: tags matter.');

    const attempt = await db
      .selectFrom('attempt.attempt')
      .selectAll()
      .where('id', '=', attemptId)
      .executeTakeFirstOrThrow();
    expect(Number(attempt.hint_penalty_total)).toBeCloseTo(0.07, 5); // 0.02 + 0.05
  });

  it('preview returns null once the ladder is exhausted (doc step 40: offer guided fallback)', async () => {
    const version = await publishWithHints();
    const attemptId = await createAndStartAttempt(version.id);

    await hints.reveal(attemptId, 't1');
    await hints.reveal(attemptId, 't1');
    const last = await hints.reveal(attemptId, 't1');
    expect(last.nextLevel).toBe(3);
    expect(last.hasMoreAfterThis).toBe(false);

    const exhausted = await hints.preview(attemptId, 't1');
    expect(exhausted).toBeNull();
  });

  it('reveal throws when the ladder is already exhausted', async () => {
    const version = await publishWithHints();
    const attemptId = await createAndStartAttempt(version.id);

    await hints.reveal(attemptId, 't1');
    await hints.reveal(attemptId, 't1');
    await hints.reveal(attemptId, 't1');

    await expect(hints.reveal(attemptId, 't1')).rejects.toThrow(
      /ladder exhausted/,
    );
  });

  it('accrued hint penalty flows into the actual score via ScoringEngineService on submit', async () => {
    const version = await publishWithHints();
    const attemptId = await createAndStartAttempt(version.id);

    await hints.reveal(attemptId, 't1'); // level 1, +0.02 penalty, matches sp.guided-lab.default's hints.perLevel.level_1

    const result = await attemptService.submit(attemptId);
    expect(result.status).toBe('PASSED'); // a small hint penalty shouldn't sink an otherwise-clean pass

    const scoreRow = await db
      .selectFrom('attempt.attempt_score')
      .selectAll()
      .where('attempt_id', '=', attemptId)
      .executeTakeFirstOrThrow();
    const penalties = scoreRow.penalties_jsonb as unknown as Record<
      string,
      number
    >;
    expect(penalties.hints).toBeCloseTo(0.02, 5);
  });
});
