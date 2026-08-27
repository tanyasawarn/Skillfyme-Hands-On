import { sql, type Kysely } from 'kysely';
import type { Database } from '../../src/db/schema';
import { AttemptRepository } from '../../src/modules/attempt/attempt.repository';
import { AttemptService } from '../../src/modules/attempt/attempt.service';
import { EligibilityService } from '../../src/modules/attempt/eligibility.service';
import { EventStoreRepository } from '../../src/modules/event-store/event-store.repository';
import { FakeOrchestratorClient } from '../../src/modules/attempt/fake-orchestrator.client';
import type { OrchestratorClient } from '../../src/modules/attempt/orchestrator-client.interface';
import { ConfigService } from '@nestjs/config';
import { SkillRepository } from '../../src/modules/skill/skill.repository';
import { MasteryService } from '../../src/modules/skill/mastery.service';
import { BktService } from '../../src/modules/skill/bkt.service';
import { EloService } from '../../src/modules/skill/elo.service';
import { CatalogRepository } from '../../src/modules/catalog/catalog.repository';
import { EvaluationService } from '../../src/modules/evaluation/evaluation.service';
import { ValidatorRunnerService } from '../../src/modules/evaluation/validator-runner.service';
import { ScoringEngineService } from '../../src/modules/evaluation/scoring-engine.service';
import { FakeValidatorExecutor } from '../../src/modules/evaluation/fake-validator-executor';
import { ReplayService } from '../../src/modules/event-store/replay.service';
import { FaultRepository } from '../../src/modules/evaluation/fault.repository';
import { ArtifactService } from '../../src/modules/evaluation/artifact.service';
import { RubricRepository } from '../../src/modules/evaluation/rubric.repository';
import { ActivitySpecReader } from '../../src/common/activity-spec-reader';
import { FakeAiGrader } from '../../src/modules/evaluation/fake-ai-grader.service';
import { createTestDb, truncateAll } from './test-db';
import { WorkspaceFileService } from '../../src/modules/attempt/workspace-file.service';

describe('AttemptService (integration, real Postgres) — doc §4.1 state machine', () => {
  let db: Kysely<Database>;
  let attemptService: AttemptService;
  let eligibility: EligibilityService;
  let catalog: CatalogRepository;
  let skillRepo: SkillRepository;
  let fakeExecutor: FakeValidatorExecutor;
  let tenantId: string;
  let userId: string;

  beforeAll(() => {
    db = createTestDb();
    const attemptRepo = new AttemptRepository(db);
    skillRepo = new SkillRepository(db);
    const mastery = new MasteryService(db, new BktService(), new EloService());
    eligibility = new EligibilityService(db, skillRepo, mastery, {
      get: () => undefined,
    } as unknown as ConfigService);
    const events = new EventStoreRepository(db);
    const orchestrator = new FakeOrchestratorClient(5);
    fakeExecutor = new FakeValidatorExecutor();
    const validatorRunner = new ValidatorRunnerService(
      db,
      events,
      fakeExecutor,
    );
    const replay = new ReplayService(db, events);
    const evaluation = new EvaluationService(
      db,
      events,
      validatorRunner,
      new ScoringEngineService(),
      mastery,
      replay,
      new FaultRepository(),
      new ArtifactService(
        db,
        events,
        new RubricRepository(),
        new FakeAiGrader(),
        new ActivitySpecReader(db),
      ),
      attemptRepo,
    );
    attemptService = new AttemptService(
      db,
      attemptRepo,
      eligibility,
      events,
      orchestrator,
      evaluation,
      validatorRunner,
      replay,
    );
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
    await skillRepo.createSkill({
      slug: 'k8s.core',
      name: 'K8s Core',
      domain: 'k8s',
    });
  });

  async function publishActivity(slug: string) {
    return catalog.publishNewVersion({
      tenantId,
      activitySlug: slug,
      mode: 'GUIDED_LAB',
      spec: {
        id: slug,
        version: 1,
        meta: { difficulty_level: 'L1', estimated_minutes: 20 },
        environment: { blueprint: 'bp.test.v1', cost_budget_usd: 0.05 },
        skills: [{ skill: 'k8s.core', weight: 1.0, primary: true }],
      },
    });
  }

  /**
   * PLAN.md Phase 2 integration point: "Fault application is triggered
   * by Dev B's Attempt Service but executed by Dev A's Orchestrator."
   * Verifies the client-side half: provision() reads a PRODUCTION_SIM
   * spec's faults array, calls injectFault() for every apply_at:"T0"
   * entry, and records FAULT_INJECTED regardless of outcome.
   */
  it('applies T0 faults after provision reaches READY and records FAULT_INJECTED per fault', async () => {
    const version = await catalog.publishNewVersion({
      tenantId,
      activitySlug: 'lab.fault-injection',
      mode: 'PRODUCTION_SIM',
      spec: {
        id: 'lab.fault-injection',
        version: 1,
        meta: { difficulty_level: 'L3', estimated_minutes: 30 },
        environment: { blueprint: 'bp.test.v1', cost_budget_usd: 0.08 },
        skills: [{ skill: 'k8s.core', weight: 1.0, primary: true }],
        faults: [
          {
            id: 'f.k8s.memory-limit-too-low',
            params: { service: 'checkout', limit: '96Mi' },
            apply_at: 'T0',
          },
          {
            id: 'f.load.traffic-spike',
            params: { rps: '400', duration_s: '180' },
            apply_at: 'T+900',
          },
        ],
      },
    });

    const attempt = await attemptService.createAttempt({
      tenantId,
      userId,
      activityVersionId: version.id,
    });
    await attemptService.provision(attempt.id);

    const events = await db
      .selectFrom('attempt.attempt_events')
      .select(['type', 'payload'])
      .where('attempt_id', '=', attempt.id)
      .where('type', '=', 'FAULT_INJECTED')
      .orderBy('seq')
      .execute();

    // Only the T0 fault applies during provision() -- T+900 is an
    // escalation timer this synchronous flow doesn't schedule yet (see
    // applyT0Faults's doc comment).
    expect(events).toHaveLength(1);
    const payload = events[0].payload as { fault_id: string; applied: boolean };
    expect(payload.fault_id).toBe('f.k8s.memory-limit-too-low');
    expect(payload.applied).toBe(true);
  });

  it('still transitions to READY even when a fault fails to apply (a platform gap must not block provisioning)', async () => {
    // A fresh orchestrator client + AttemptService instance so the
    // fault-outcome override below doesn't leak into other tests
    // sharing the describe block's orchestrator instance.
    const orchestratorForThisTest = new FakeOrchestratorClient(5);
    orchestratorForThisTest.setFaultOverride('f.does-not-exist', {
      applied: false,
      symptomVerified: false,
    });

    const attemptRepo = new AttemptRepository(db);
    const events = new EventStoreRepository(db);
    const evaluation = new EvaluationService(
      db,
      events,
      new ValidatorRunnerService(db, events, new FakeValidatorExecutor()),
      new ScoringEngineService(),
      new MasteryService(db, new BktService(), new EloService()),
      new ReplayService(db, events),
      new FaultRepository(),
      new ArtifactService(
        db,
        events,
        new RubricRepository(),
        new FakeAiGrader(),
        new ActivitySpecReader(db),
      ),
      attemptRepo,
    );
    const isolatedAttemptService = new AttemptService(
      db,
      attemptRepo,
      eligibility,
      events,
      orchestratorForThisTest,
      evaluation,
      new ValidatorRunnerService(db, events, new FakeValidatorExecutor()),
      new ReplayService(db, events),
    );

    const version = await catalog.publishNewVersion({
      tenantId,
      activitySlug: 'lab.fault-injection-failure',
      mode: 'PRODUCTION_SIM',
      spec: {
        id: 'lab.fault-injection-failure',
        version: 1,
        meta: { difficulty_level: 'L3', estimated_minutes: 30 },
        environment: { blueprint: 'bp.test.v1', cost_budget_usd: 0.08 },
        skills: [{ skill: 'k8s.core', weight: 1.0, primary: true }],
        faults: [{ id: 'f.does-not-exist', params: {}, apply_at: 'T0' }],
      },
    });

    const attempt = await isolatedAttemptService.createAttempt({
      tenantId,
      userId,
      activityVersionId: version.id,
    });
    const provisioned = await isolatedAttemptService.provision(attempt.id);
    expect(provisioned.status).toBe('READY');

    const faultEvent = await db
      .selectFrom('attempt.attempt_events')
      .select(['payload'])
      .where('attempt_id', '=', attempt.id)
      .where('type', '=', 'FAULT_INJECTED')
      .executeTakeFirstOrThrow();
    expect((faultEvent.payload as { applied: boolean }).applied).toBe(false);
  });

  it('walks the full CREATED -> READY -> IN_PROGRESS -> SUBMITTED path with correct events', async () => {
    const version = await publishActivity('lab.walkthrough');

    const attempt = await attemptService.createAttempt({
      tenantId,
      userId,
      activityVersionId: version.id,
    });
    expect(attempt.status).toBe('CREATED');

    const provisioned = await attemptService.provision(attempt.id);
    expect(provisioned.status).toBe('READY');
    expect(provisioned.environment_id).toMatch(/^fake-env-/);

    const started = await attemptService.markStarted(attempt.id);
    expect(started.status).toBe('IN_PROGRESS');
    expect(started.started_at).not.toBeNull();

    // submit() now runs evaluation synchronously (FakeOrchestratorClient
    // has no real environment to tear down, so there's no ENV_DESTROYED
    // event to wait on -- see the doc comment on AttemptService.submit()).
    // The returned attempt reflects the *post-evaluation* state, not the
    // transient SUBMITTED status.
    const submitted = await attemptService.submit(attempt.id);
    expect(['PASSED', 'FAILED']).toContain(submitted.status);
    expect(submitted.submitted_at).not.toBeNull();
    expect(submitted.completed_at).not.toBeNull();

    const eventLog = await db
      .selectFrom('attempt.attempt_events')
      .select(['type', 'actor'])
      .where('attempt_id', '=', attempt.id)
      .orderBy('seq')
      .execute();
    // lab.walkthrough was published with no tasks (see publishActivity
    // helper), so evaluation runs zero validators -- only the
    // request/complete bookends appear, not per-task VALIDATOR_RESULT/
    // TASK_PASSED events.
    expect(eventLog.map((e) => e.type)).toEqual([
      'ATTEMPT_CREATED',
      'ENV_REQUESTED',
      'ENV_READY',
      'ATTEMPT_STARTED',
      'SUBMITTED',
      'VALIDATION_REQUESTED',
      'EVALUATED',
    ]);
  });

  // PLAN_RPC_AUTHZ.md Section 4/5: the orchestrator's Connect/Destroy RPCs
  // now enforce that the caller-supplied attempt_id owns environment_id
  // (orchestrator/internal/orchestrator/server.go's checkEnvironmentOwnership).
  // That check is worthless if AttemptService never actually sends
  // attempt_id -- FakeOrchestratorClient itself doesn't validate its
  // input, so the rest of this suite passing would NOT catch a missing
  // attemptId. This test wraps it with a spy to assert the real payload,
  // and is also the first test in this file to exercise
  // AttemptService.connect() at all (previously uncovered).
  it('forwards attemptId to the orchestrator on both connect() and the submit-triggered destroy()', async () => {
    const calls: { method: string; req: unknown }[] = [];
    const spyOrchestrator: OrchestratorClient = {
      provision: (req) => orchestratorForSpy.provision(req),
      destroy: (req) => {
        calls.push({ method: 'destroy', req });
        return orchestratorForSpy.destroy(req);
      },
      connect: (req) => {
        calls.push({ method: 'connect', req });
        return orchestratorForSpy.connect(req);
      },
      injectFault: (req) => orchestratorForSpy.injectFault(req),
      execShell: (req) => orchestratorForSpy.execShell(req),
    };
    const orchestratorForSpy = new FakeOrchestratorClient(5);
    const attemptRepoForSpy = new AttemptRepository(db);
    const eventsForSpy = new EventStoreRepository(db);
    const mastery = new MasteryService(db, new BktService(), new EloService());
    const validatorRunnerForSpy = new ValidatorRunnerService(
      db,
      eventsForSpy,
      new FakeValidatorExecutor(),
    );
    const evaluationForSpy = new EvaluationService(
      db,
      eventsForSpy,
      validatorRunnerForSpy,
      new ScoringEngineService(),
      mastery,
      new ReplayService(db, eventsForSpy),
      new FaultRepository(),
      new ArtifactService(
        db,
        eventsForSpy,
        new RubricRepository(),
        new FakeAiGrader(),
        new ActivitySpecReader(db),
      ),
      attemptRepoForSpy,
    );
    const attemptServiceForSpy = new AttemptService(
      db,
      attemptRepoForSpy,
      eligibility,
      eventsForSpy,
      spyOrchestrator,
      evaluationForSpy,
      validatorRunnerForSpy,
      new ReplayService(db, eventsForSpy),
    );

    const version = await publishActivity('lab.walkthrough');
    const attempt = await attemptServiceForSpy.createAttempt({
      tenantId,
      userId,
      activityVersionId: version.id,
    });
    const provisioned = await attemptServiceForSpy.provision(attempt.id);

    await attemptServiceForSpy.connect(attempt.id);
    const connectCall = calls.find((c) => c.method === 'connect');
    expect(connectCall?.req).toMatchObject({
      environmentId: provisioned.environment_id,
      attemptId: attempt.id,
    });

    await attemptServiceForSpy.markStarted(attempt.id);
    await attemptServiceForSpy.submit(attempt.id);
    const destroyCall = calls.find((c) => c.method === 'destroy');
    expect(destroyCall?.req).toMatchObject({
      environmentId: provisioned.environment_id,
      attemptId: attempt.id,
    });
  });

  // WorkspaceFileService had zero test coverage of any kind before this
  // (confirmed via repo-wide grep) -- this both closes that gap and
  // proves its 3 execShell call sites (list/read/write) all forward
  // attemptId, the same real-payload-not-just-compiles verification
  // applied to connect()/destroy() above.
  it('WorkspaceFileService forwards attemptId on list/read/write execShell calls', async () => {
    const calls: { method: string; req: { attemptId?: string } }[] = [];
    const spyOrchestrator: OrchestratorClient = {
      provision: (req) => orchestratorForSpy.provision(req),
      destroy: (req) => orchestratorForSpy.destroy(req),
      connect: (req) => orchestratorForSpy.connect(req),
      injectFault: (req) => orchestratorForSpy.injectFault(req),
      execShell: (req) => {
        calls.push({ method: 'execShell', req });
        return orchestratorForSpy.execShell(req);
      },
    };
    const orchestratorForSpy = new FakeOrchestratorClient(5);
    const attemptRepoForSpy = new AttemptRepository(db);
    const workspaceFiles = new WorkspaceFileService(
      attemptRepoForSpy,
      spyOrchestrator,
    );

    const version = await publishActivity('lab.walkthrough');
    const eventsForSpy = new EventStoreRepository(db);
    const mastery = new MasteryService(db, new BktService(), new EloService());
    const evaluationForSpy = new EvaluationService(
      db,
      eventsForSpy,
      new ValidatorRunnerService(db, eventsForSpy, new FakeValidatorExecutor()),
      new ScoringEngineService(),
      mastery,
      new ReplayService(db, eventsForSpy),
      new FaultRepository(),
      new ArtifactService(
        db,
        eventsForSpy,
        new RubricRepository(),
        new FakeAiGrader(),
        new ActivitySpecReader(db),
      ),
      attemptRepoForSpy,
    );
    const attemptServiceForSpy = new AttemptService(
      db,
      attemptRepoForSpy,
      eligibility,
      eventsForSpy,
      orchestratorForSpy,
      evaluationForSpy,
      new ValidatorRunnerService(db, eventsForSpy, new FakeValidatorExecutor()),
      new ReplayService(db, eventsForSpy),
    );
    const attempt = await attemptServiceForSpy.createAttempt({
      tenantId,
      userId,
      activityVersionId: version.id,
    });
    await attemptServiceForSpy.provision(attempt.id);

    await workspaceFiles.list(attempt.id, '.');
    await workspaceFiles.read(attempt.id, 'foo.txt').catch(() => undefined); // FakeOrchestratorClient always reports "no filesystem" -- read()/write() throw BadRequestException on that, which is fine, the RPC call itself still happened
    await workspaceFiles
      .write(attempt.id, 'foo.txt', 'hello')
      .catch(() => undefined);

    expect(calls).toHaveLength(3);
    for (const call of calls) {
      expect(call.req.attemptId).toBe(attempt.id);
    }
  });

  it('is idempotent on duplicate create with the same idempotency key (doc §4.4)', async () => {
    const version = await publishActivity('lab.idempotent');

    const first = await attemptService.createAttempt({
      tenantId,
      userId,
      activityVersionId: version.id,
      idempotencyKey: 'key-1',
    });
    const second = await attemptService.createAttempt({
      tenantId,
      userId,
      activityVersionId: version.id,
      idempotencyKey: 'key-1',
    });

    expect(second.id).toBe(first.id);

    const count = await db
      .selectFrom('attempt.attempt')
      .select((eb) => eb.fn.countAll<number>().as('c'))
      .where('user_id', '=', userId)
      .executeTakeFirstOrThrow();
    expect(Number(count.c)).toBe(1);

    const createdEvents = await db
      .selectFrom('attempt.attempt_events')
      .selectAll()
      .where('attempt_id', '=', first.id)
      .where('type', '=', 'ATTEMPT_CREATED')
      .execute();
    expect(createdEvents).toHaveLength(1); // not double-appended on the duplicate call
  });

  /**
   * Doc §2.7's early-clear clause ("cleared early if the learner passes
   * a remediation activity for the failed sub-skill") isn't implemented
   * yet -- that depends on the recommendation engine's remediation
   * ladder, Phase 4 scope. Tests that need to exercise createAttempt
   * repeatedly without waiting out a real cooldown clear it directly,
   * standing in for what a real remediation-pass would eventually do.
   */
  async function clearCooldown(activityId: string) {
    await db
      .updateTable('learner.learner_activity_state')
      .set({ cooldown_until: null })
      .where('user_id', '=', userId)
      .where('activity_id', '=', activityId)
      .execute();
  }

  it('chains a retry: createAttempt sets retry_of_attempt_id + incremented retry_index after a prior completed attempt on the same activity', async () => {
    // Doc §2.7 / §4.5: "A retry is a new Attempt row with
    // retry_of_attempt_id and incremented retry_index." lab.walkthrough-
    // style activities (no tasks) reliably fail (task_completion/
    // technical_correctness both have nothing to pass), so submitting is
    // a deterministic way to produce a completed attempt to retry from.
    const version = await publishActivity('lab.retry-chain');

    const first = await attemptService.createAttempt({
      tenantId,
      userId,
      activityVersionId: version.id,
    });
    expect(first.retry_index).toBe(0);
    expect(first.retry_of_attempt_id).toBeNull();
    await attemptService.provision(first.id);
    await attemptService.markStarted(first.id);
    const firstDone = await attemptService.submit(first.id);
    expect(firstDone.status).toBe('FAILED');

    await clearCooldown(first.activity_id);
    const second = await attemptService.createAttempt({
      tenantId,
      userId,
      activityVersionId: version.id,
      idempotencyKey: 'retry-2',
    });
    expect(second.retry_of_attempt_id).toBe(first.id);
    expect(second.retry_index).toBe(1);

    await attemptService.provision(second.id);
    await attemptService.markStarted(second.id);
    const secondDone = await attemptService.submit(second.id);
    expect(secondDone.status).toBe('FAILED');

    await clearCooldown(first.activity_id);
    const third = await attemptService.createAttempt({
      tenantId,
      userId,
      activityVersionId: version.id,
      idempotencyKey: 'retry-3',
    });
    expect(third.retry_of_attempt_id).toBe(second.id);
    expect(third.retry_index).toBe(2);
  });

  // PLAN.md K7 regression: AttemptRepository.findMostRecentCompletedAttempt's
  // status list used to be hand-copied separately from
  // AttemptService.TERMINAL_STATUSES and had silently drifted to omit
  // PROVISION_FAILED -- an attempt that failed to provision was
  // invisible to retry-chaining, with no documented reason for the
  // difference from the terminal-status check elsewhere. Now both derive
  // from the shared AttemptStatusGroups.
  it('chains a retry after a PROVISION_FAILED attempt (the drift K7 closes)', async () => {
    const version = await publishActivity('lab.retry-after-provision-failure');
    const attemptRepo = new AttemptRepository(db);

    const first = await attemptService.createAttempt({
      tenantId,
      userId,
      activityVersionId: version.id,
    });
    await attemptRepo.transition(first.id, first.version, {
      status: 'PROVISION_FAILED',
    });

    const second = await attemptService.createAttempt({
      tenantId,
      userId,
      activityVersionId: version.id,
      idempotencyKey: 'retry-after-provision-failure-2',
    });
    expect(second.retry_of_attempt_id).toBe(first.id);
    expect(second.retry_index).toBe(1);
  });

  it('does not treat an in-flight (non-completed) attempt as something to retry from', async () => {
    const version = await publishActivity('lab.retry-in-flight');
    const first = await attemptService.createAttempt({
      tenantId,
      userId,
      activityVersionId: version.id,
    });
    expect(first.retry_index).toBe(0);
    // first is still CREATED here (never provisioned/submitted) -- a
    // second createAttempt call for a *different* idempotency key would
    // be blocked by the concurrent-environment quota once provisioned,
    // but at CREATED (pre-provision) it isn't yet, so this checks the
    // retry-chain logic specifically doesn't pick up a not-yet-completed
    // attempt as a "prior attempt to retry."
    const second = await attemptService.createAttempt({
      tenantId,
      userId,
      activityVersionId: version.id,
      idempotencyKey: 'not-a-retry',
    });
    expect(second.retry_of_attempt_id).toBeNull();
    expect(second.retry_index).toBe(0);
  });

  it('a failed attempt sets learner_activity_state.cooldown_until (doc §2.7) and blocks a re-attempt until it clears', async () => {
    const version = await publishActivity('lab.cooldown');
    const attempt = await attemptService.createAttempt({
      tenantId,
      userId,
      activityVersionId: version.id,
    });
    await attemptService.provision(attempt.id);
    await attemptService.markStarted(attempt.id);
    const done = await attemptService.submit(attempt.id);
    expect(done.status).toBe('FAILED');

    const state = await db
      .selectFrom('learner.learner_activity_state')
      .selectAll()
      .where('user_id', '=', userId)
      .where('activity_id', '=', attempt.activity_id)
      .executeTakeFirstOrThrow();
    expect(state.cooldown_until).not.toBeNull();
    // First failure (retry_index=0 on the failed attempt) -> base 24h cooldown.
    const hoursUntilCooldownEnds =
      (new Date(state.cooldown_until as unknown as string).getTime() -
        Date.now()) /
      (60 * 60 * 1000);
    expect(hoursUntilCooldownEnds).toBeGreaterThan(23.9);
    expect(hoursUntilCooldownEnds).toBeLessThanOrEqual(24);

    const check = await eligibility.check(userId, version.id);
    expect(check.eligible).toBe(false);
    expect(check.reasons.some((r) => r.code === 'COOLDOWN_ACTIVE')).toBe(true);
  });

  it('a passed attempt clears any cooldown left by a prior failure on the same activity', async () => {
    const version = await publishActivity('lab.cooldown-cleared');

    // Force the first attempt to fail via a task the fake executor is
    // told to fail, and the second to pass by not overriding it -- both
    // need at least one task+validator so task_completion/technical_
    // correctness can actually swing pass/fail deterministically (an
    // activity with zero tasks always fails regardless, per the other
    // tests above, which wouldn't let this test prove anything about a
    // later *pass* clearing the cooldown).
    const versionWithTask = await catalog.publishNewVersion({
      tenantId,
      activitySlug: 'lab.cooldown-cleared-2',
      mode: 'GUIDED_LAB',
      spec: {
        id: 'lab.cooldown-cleared-2',
        version: 1,
        meta: { difficulty_level: 'L1', estimated_minutes: 20 },
        environment: { blueprint: 'bp.test.v1', cost_budget_usd: 0.05 },
        skills: [{ skill: 'k8s.core', weight: 1.0, primary: true }],
        tasks: [
          {
            key: 't1',
            title: 'Task 1',
            required: true,
            instructions_md: 'x',
            validators: [
              {
                id: 'v1',
                type: 'SHELL_ASSERT',
                run: 'true',
                expect: {},
                weight: 1,
                on_fail: 'try again',
              },
            ],
          },
        ],
      },
    });

    const failing = await attemptService.createAttempt({
      tenantId,
      userId,
      activityVersionId: versionWithTask.id,
    });
    fakeExecutor.setOverride(
      (await attemptService.provision(failing.id)).environment_id!,
      'v1',
      'FAIL',
    );
    await attemptService.markStarted(failing.id);
    const failed = await attemptService.submit(failing.id);
    expect(failed.status).toBe('FAILED');

    const cooledDown = await db
      .selectFrom('learner.learner_activity_state')
      .selectAll()
      .where('user_id', '=', userId)
      .where('activity_id', '=', failing.activity_id)
      .executeTakeFirstOrThrow();
    expect(cooledDown.cooldown_until).not.toBeNull();

    // Now pass on the retry -- fake executor defaults to PASS with no
    // override. The cooldown from the failure above would otherwise
    // block createAttempt outright; clear it the same way a real
    // remediation-activity pass eventually would (§2.7's early-clear
    // clause, not yet implemented -- see clearCooldown's doc comment).
    await clearCooldown(failing.activity_id);
    const passing = await attemptService.createAttempt({
      tenantId,
      userId,
      activityVersionId: versionWithTask.id,
      idempotencyKey: 'now-pass',
    });
    await attemptService.provision(passing.id);
    await attemptService.markStarted(passing.id);
    const passed = await attemptService.submit(passing.id);
    expect(passed.status).toBe('PASSED');

    const cleared = await db
      .selectFrom('learner.learner_activity_state')
      .selectAll()
      .where('user_id', '=', userId)
      .where('activity_id', '=', failing.activity_id)
      .executeTakeFirstOrThrow();
    expect(cleared.cooldown_until).toBeNull();
  });

  it('rejects a second attempt while the first still holds the concurrent-environment slot (doc §4.4), across every active status', async () => {
    const version = await publishActivity('lab.quota');
    const attempt = await attemptService.createAttempt({
      tenantId,
      userId,
      activityVersionId: version.id,
    });

    // CREATED does not yet hold the environment slot -- provisioning does.
    await expect(
      attemptService.createAttempt({
        tenantId,
        userId,
        activityVersionId: version.id,
        idempotencyKey: 'other',
      }),
    ).resolves.toBeDefined();

    // Once provisioned (READY) through IN_PROGRESS, the slot is held --
    // this is the concurrency bug originally caught by manual boot-testing.
    await attemptService.provision(attempt.id);
    let result = await eligibility.check(userId, version.id);
    expect(result.eligible).toBe(false);
    expect(
      result.reasons.some((r) => r.code === 'CONCURRENT_QUOTA_EXCEEDED'),
    ).toBe(true);

    await attemptService.markStarted(attempt.id);
    result = await eligibility.check(userId, version.id);
    expect(result.eligible).toBe(false);

    // submit() evaluates synchronously in Phase 1 (see the doc comment on
    // AttemptService.submit()), landing the attempt directly on
    // PASSED/FAILED -- neither is in the eligibility gate's active-status
    // list (doc §4.1's SUBMITTED/EVALUATING window only applies to the
    // real-orchestrator path, where teardown happens after grading, not
    // before it). The slot is correctly freed once evaluation completes.
    // lab.quota (via publishActivity) has no tasks, so it deterministically
    // fails -- which now (doc §2.7) also sets a retry cooldown, a second,
    // legitimate ineligibility reason unrelated to the concurrency slot
    // this test is actually about. Assert on the concurrency reason
    // specifically clearing, not overall eligibility.
    await attemptService.submit(attempt.id);
    result = await eligibility.check(userId, version.id);
    expect(
      result.reasons.some((r) => r.code === 'CONCURRENT_QUOTA_EXCEEDED'),
    ).toBe(false);
  });

  it('rejects createAttempt outright when eligibility fails (BadRequestException with reasons)', async () => {
    const version = await publishActivity('lab.reject-test');
    await attemptService.createAttempt({
      tenantId,
      userId,
      activityVersionId: version.id,
    });
    await attemptService.provision(
      (
        await attemptService.createAttempt({
          tenantId,
          userId,
          activityVersionId: version.id,
        })
      ).id,
    );

    await expect(
      attemptService.createAttempt({
        tenantId,
        userId,
        activityVersionId: version.id,
        idempotencyKey: 'blocked',
      }),
    ).rejects.toThrow(/concurrent-environment/);
  });

  it('handleEnvironmentDestroyed does not regress an already-evaluated attempt (guards every terminal status)', async () => {
    // Phase 1's submit() evaluates synchronously (FakeOrchestratorClient
    // has no real environment to wait on), so by the time a stray
    // ENV_DESTROYED could arrive -- e.g. the orchestrator's idle detector
    // or reaper independently racing the same environment submit() just
    // asked to be destroyed -- the attempt is already PASSED/FAILED, a
    // terminal state. transition() itself has no state-machine check (a
    // plain version-gated update), so this guard is the only thing
    // stopping a late destroy event from silently regressing a finished
    // attempt back to EVALUATING/SUSPENDED.
    const version = await publishActivity('lab.env-destroyed');
    const attempt = await attemptService.createAttempt({
      tenantId,
      userId,
      activityVersionId: version.id,
    });
    await attemptService.provision(attempt.id);
    await attemptService.markStarted(attempt.id);
    const submitted = await attemptService.submit(attempt.id);
    expect(['PASSED', 'FAILED']).toContain(submitted.status);

    await attemptService.handleEnvironmentDestroyed(attempt.id, 'submit');
    const after = await db
      .selectFrom('attempt.attempt')
      .selectAll()
      .where('id', '=', attempt.id)
      .executeTakeFirstOrThrow();
    expect(after.status).toBe(submitted.status); // unchanged, not regressed to EVALUATING

    // Idempotent regardless of reason.
    await expect(
      attemptService.handleEnvironmentDestroyed(attempt.id, 'idle'),
    ).resolves.not.toThrow();
  });

  it('handleEnvironmentDestroyed(reason=idle) suspends an IN_PROGRESS attempt and frees the concurrent-environment slot', async () => {
    // This is the actual bug this feature exists to fix: before
    // ENV_DESTROYED was wired end-to-end, an idle-timeout or
    // TTL-expired environment left the attempt stuck IN_PROGRESS
    // forever, permanently blocking the next lab via the
    // concurrent-environment quota below -- even though the real
    // infrastructure was long gone. SUSPENDED is deliberately absent
    // from EligibilityService's active-status list, so this transition
    // alone is what unblocks the learner.
    const version = await publishActivity('lab.idle-destroy');
    const attempt = await attemptService.createAttempt({
      tenantId,
      userId,
      activityVersionId: version.id,
    });
    await attemptService.provision(attempt.id);
    await attemptService.markStarted(attempt.id);

    const blocked = await eligibility.check(userId, version.id);
    expect(blocked.eligible).toBe(false);
    expect(
      blocked.reasons.some((r) => r.code === 'CONCURRENT_QUOTA_EXCEEDED'),
    ).toBe(true);

    await attemptService.handleEnvironmentDestroyed(attempt.id, 'idle');

    const after = await db
      .selectFrom('attempt.attempt')
      .selectAll()
      .where('id', '=', attempt.id)
      .executeTakeFirstOrThrow();
    expect(after.status).toBe('SUSPENDED');
    expect(after.environment_id).toBeNull();

    const unblocked = await eligibility.check(userId, version.id);
    expect(unblocked.eligible).toBe(true);

    // Idempotent: a second idle/ttl/reaper event for the same
    // already-suspended attempt must not throw or double-transition.
    await expect(
      attemptService.handleEnvironmentDestroyed(attempt.id, 'reaper'),
    ).resolves.not.toThrow();
  });

  it('throws ConflictException on a stale-version transition (optimistic concurrency, doc §4.4)', async () => {
    const version = await publishActivity('lab.concurrency');
    const attempt = await attemptService.createAttempt({
      tenantId,
      userId,
      activityVersionId: version.id,
    });
    const attemptRepo = new AttemptRepository(db);

    // Simulate a concurrent writer bumping the version first.
    await attemptRepo.transition(attempt.id, attempt.version, {
      status: 'PROVISIONING',
    });

    // Now attempt a transition using the stale (pre-bump) version number.
    await expect(
      attemptRepo.transition(attempt.id, attempt.version, { status: 'READY' }),
    ).rejects.toThrow(/modified concurrently/);
  });

  /**
   * Revised lifecycle requirement §3/§9's two-stage flow: active ->
   * (15min idle, already covered by the 'handleEnvironmentDestroyed
   * suspends an IN_PROGRESS attempt' test above) -> suspended -> (this
   * sweep) -> cached. cache() is CacheSweepService's per-attempt action;
   * the sweep loop itself is just "find stale SUSPENDED rows, call
   * cache() on each" (see cache-sweep.service.ts), so exercising cache()
   * directly against a real Postgres row is the meaningful integration
   * point -- the interval timer itself is plain JS with nothing
   * DB-specific to verify.
   */
  it('cache() moves a stale SUSPENDED attempt to CACHED with a stub snapshot, and is idempotent', async () => {
    const version = await publishActivity('lab.cache-sweep');
    const attempt = await attemptService.createAttempt({
      tenantId,
      userId,
      activityVersionId: version.id,
    });
    await attemptService.provision(attempt.id);
    await attemptService.markStarted(attempt.id);

    const attemptRepo = new AttemptRepository(db);

    // cache() only ever reads from SUSPENDED (two-stage design) -- an
    // attempt gets there via the real path first: the orchestrator's
    // idle detector (or ttl/reaper/budget/admin) destroying the
    // environment and publishing ENV_DESTROYED, which this handler
    // turns into SUSPENDED. Not a direct IN_PROGRESS -> CACHED jump.
    await attemptService.handleEnvironmentDestroyed(attempt.id, 'idle');
    const suspended = await attemptRepo.findById(attempt.id);
    expect(suspended?.status).toBe('SUSPENDED');

    // Simulate staying suspended past the second-stage threshold,
    // rather than waiting -- this is the same last_activity_at column
    // CacheSweepService's WHERE clause reads (findStaleSuspendedAttempts).
    const staleTimestamp = new Date(Date.now() - 73 * 60 * 60 * 1000);
    await sql`UPDATE attempt.attempt SET last_activity_at = ${staleTimestamp} WHERE id = ${attempt.id}`.execute(
      db,
    );

    await attemptService.cache(attempt.id);

    const cached = await attemptRepo.findById(attempt.id);
    expect(cached?.status).toBe('CACHED');
    expect(cached?.environment_id).toBeNull();
    // Snapshot stub (§5): a placeholder id/timestamp is recorded so the
    // data model is ready for real capture, even though no real
    // workspace-state persistence exists yet (see cache()'s doc comment).
    expect(cached?.snapshot_id).toMatch(/^stub-/);
    expect(cached?.snapshot_taken_at).not.toBeNull();

    const snapshotEvent = await db
      .selectFrom('attempt.attempt_events')
      .selectAll()
      .where('attempt_id', '=', attempt.id)
      .where('type', '=', 'SNAPSHOT_TAKEN')
      .executeTakeFirstOrThrow();
    expect((snapshotEvent.payload as { stub: boolean }).stub).toBe(true);

    // Idempotent: calling cache() again on an already-CACHED attempt is a no-op.
    await expect(attemptService.cache(attempt.id)).resolves.not.toThrow();
    const stillCached = await attemptRepo.findById(attempt.id);
    expect(stillCached?.status).toBe('CACHED');
    expect(stillCached?.version).toBe(cached!.version); // no second transition happened
  });

  /**
   * The real regression handleEnvironmentDestroyed's own doc comment
   * flags: cache()'s (former) orchestrator.destroy() call used to cause
   * the orchestrator to loop an ENV_DESTROYED(idle) back through this
   * handler, regressing a just-cached attempt back to SUSPENDED. Fixed
   * by (a) excluding CACHED from handleEnvironmentDestroyed the same way
   * SUSPENDED already was, and (b) the two-stage design meaning cache()
   * no longer calls orchestrator.destroy() at all (the environment is
   * already gone by the time an attempt reaches SUSPENDED). This test
   * covers (a) directly, simulating the stray event cache() used to
   * indirectly trigger.
   */
  it('handleEnvironmentDestroyed does not regress a CACHED attempt back to SUSPENDED', async () => {
    const version = await publishActivity('lab.cached-no-regress');
    const attempt = await attemptService.createAttempt({
      tenantId,
      userId,
      activityVersionId: version.id,
    });
    await attemptService.provision(attempt.id);
    await attemptService.handleEnvironmentDestroyed(attempt.id, 'idle');

    const staleTimestamp = new Date(Date.now() - 73 * 60 * 60 * 1000);
    await sql`UPDATE attempt.attempt SET last_activity_at = ${staleTimestamp} WHERE id = ${attempt.id}`.execute(
      db,
    );
    await attemptService.cache(attempt.id);

    const attemptRepo = new AttemptRepository(db);
    const cached = await attemptRepo.findById(attempt.id);
    expect(cached?.status).toBe('CACHED');

    // A stray/late ENV_DESTROYED for the same environment must not
    // regress it back to SUSPENDED.
    await attemptService.handleEnvironmentDestroyed(attempt.id, 'idle');
    const stillCached = await attemptRepo.findById(attempt.id);
    expect(stillCached?.status).toBe('CACHED');
    expect(stillCached?.version).toBe(cached!.version);
  });

  it('reactivate() resumes a CACHED attempt back to CREATED, and a second click is a no-op (idempotent)', async () => {
    const version = await publishActivity('lab.reactivate');
    const attempt = await attemptService.createAttempt({
      tenantId,
      userId,
      activityVersionId: version.id,
    });
    await attemptService.provision(attempt.id);
    await attemptService.handleEnvironmentDestroyed(attempt.id, 'idle');

    const attemptRepo = new AttemptRepository(db);
    const staleTimestamp = new Date(Date.now() - 73 * 60 * 60 * 1000);
    await sql`UPDATE attempt.attempt SET last_activity_at = ${staleTimestamp} WHERE id = ${attempt.id}`.execute(
      db,
    );
    await attemptService.cache(attempt.id);

    const reactivated = await attemptService.reactivate(attempt.id);
    expect(reactivated.status).toBe('CREATED');
    expect(reactivated.environment_id).toBeNull();

    const resumedEvent = await db
      .selectFrom('attempt.attempt_events')
      .selectAll()
      .where('attempt_id', '=', attempt.id)
      .where('type', '=', 'RESUMED')
      .executeTakeFirstOrThrow();
    expect(resumedEvent).toBeDefined();

    // "multiple clicks should not trigger duplicate provisioning" --
    // reactivate() on an attempt already past CACHED is a pure no-op,
    // not a second CREATED->CREATED transition (which would also be
    // harmless, but this confirms it doesn't even try).
    const second = await attemptService.reactivate(attempt.id);
    expect(second.version).toBe(reactivated.version);

    // The normal path back into an environment still works from here.
    const reprovisioned = await attemptService.provision(attempt.id);
    expect(reprovisioned.status).toBe('READY');
    expect(await attemptRepo.findById(attempt.id)).toMatchObject({
      status: 'READY',
    });
  });

  it('reactivate() also resumes a PROVISION_FAILED attempt (recoverable -- infra never came up, learner did nothing wrong)', async () => {
    // FakeOrchestratorClient always returns READY -- there's no existing
    // knob to force PROVISION_FAILED through it, and adding one would
    // change a fixture every other test in this file also depends on.
    // A minimal one-off fake, scoped to this test only, is the smaller
    // change: fails provision() exactly once, then behaves normally.
    let provisionCalls = 0;
    const flakyOrchestrator: OrchestratorClient = {
      async provision() {
        provisionCalls += 1;
        if (provisionCalls === 1) {
          return { environmentId: '', status: 'PROVISION_FAILED' };
        }
        return {
          environmentId: `fake-env-retry-${provisionCalls}`,
          status: 'READY',
        };
      },
      async destroy() {
        return { alreadyDestroyed: false };
      },
      async connect() {
        throw new Error('not used in this test');
      },
      async injectFault() {
        return { applied: true, symptomVerified: true };
      },
      async execShell() {
        return { exitCode: 1, stdout: '', stderr: 'not used in this test' };
      },
    };

    const attemptRepo = new AttemptRepository(db);
    const events = new EventStoreRepository(db);
    const evaluation = new EvaluationService(
      db,
      events,
      new ValidatorRunnerService(db, events, new FakeValidatorExecutor()),
      new ScoringEngineService(),
      new MasteryService(db, new BktService(), new EloService()),
      new ReplayService(db, events),
      new FaultRepository(),
      new ArtifactService(
        db,
        events,
        new RubricRepository(),
        new FakeAiGrader(),
        new ActivitySpecReader(db),
      ),
      attemptRepo,
    );
    const isolatedAttemptService = new AttemptService(
      db,
      attemptRepo,
      eligibility,
      events,
      flakyOrchestrator,
      evaluation,
      new ValidatorRunnerService(db, events, new FakeValidatorExecutor()),
      new ReplayService(db, events),
    );

    const version = await publishActivity('lab.provision-failed-recovery');
    const attempt = await isolatedAttemptService.createAttempt({
      tenantId,
      userId,
      activityVersionId: version.id,
    });

    const failed = await isolatedAttemptService.provision(attempt.id);
    expect(failed.status).toBe('PROVISION_FAILED');

    const reactivated = await isolatedAttemptService.reactivate(attempt.id);
    expect(reactivated.status).toBe('CREATED');

    const reprovisioned = await isolatedAttemptService.provision(attempt.id);
    expect(reprovisioned.status).toBe('READY');
  });
});
