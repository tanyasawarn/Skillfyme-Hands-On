import { Inject, Injectable, Logger } from '@nestjs/common';
import { Kysely, sql } from 'kysely';
import { KYSELY } from '../../db/database.module';
import type { Database } from '../../db/schema';
import { EventStoreRepository } from '../event-store/event-store.repository';
import { ReplayService } from '../event-store/replay.service';
import { MasteryService } from '../skill/mastery.service';
import {
  ValidatorRunnerService,
  type TaskSpec,
} from './validator-runner.service';
import { ScoringEngineService } from './scoring-engine.service';
import { GUIDED_LAB_DEFAULT_PROFILE } from './scoring-profile';
import type { CriterionInput } from './criteria';

/**
 * Doc §4.1 step 11: "Evaluation Engine: collect signals -> apply rubric
 * v_n -> score -> feedback. AI portions run async; deterministic portion
 * is instant." Phase 1 has no AI-graded criteria (Production Sim/Project
 * only, §6.5), so this is the deterministic path end-to-end: run all
 * validators against the submitted attempt's environment, score via
 * ScoringEngineService, persist attempt_score, update BKT mastery per
 * mapped skill (§2.4 step 5, weighted by activity_skill.weight per §2.7
 * "evidence apportioned to skills by activity_skill.weight").
 */
@Injectable()
export class EvaluationService {
  private readonly logger = new Logger(EvaluationService.name);

  constructor(
    @Inject(KYSELY) private readonly db: Kysely<Database>,
    private readonly events: EventStoreRepository,
    private readonly validatorRunner: ValidatorRunnerService,
    private readonly scoringEngine: ScoringEngineService,
    private readonly mastery: MasteryService,
    private readonly replay: ReplayService,
  ) {}

  async evaluate(
    attemptId: string,
  ): Promise<{ finalScore: number; passed: boolean }> {
    const attempt = await this.db
      .selectFrom('attempt.attempt')
      .selectAll()
      .where('id', '=', attemptId)
      .executeTakeFirstOrThrow();

    if (attempt.status !== 'EVALUATING' && attempt.status !== 'SUBMITTED') {
      throw new Error(
        `attempt ${attemptId} is ${attempt.status}, expected SUBMITTED/EVALUATING`,
      );
    }

    const version = await this.db
      .selectFrom('content.activity_version')
      .selectAll()
      .where('id', '=', attempt.activity_version_id)
      .executeTakeFirstOrThrow();

    const specRaw = version.spec_jsonb as {
      tasks?: TaskSpec[];
      environment?: { tier?: string };
    };
    // Defensive default: activity_spec.schema.json (contracts/) requires
    // tasks to be present with >=1 entry for a real published activity,
    // but nothing in this codebase enforces that schema at the DB write
    // path yet (only SpecLintService does, and only when a spec is
    // published through it, not through raw CatalogRepository calls in
    // tests/scripts). An activity with no tasks evaluates as zero
    // required tasks -> ScoringEngineService's task_completion criterion
    // returns 1.0 ("no required tasks") per its own doc comment, not a
    // crash.
    const spec = {
      tasks: specRaw.tasks ?? [],
      environment: specRaw.environment,
    };

    // Doc §6.3: validation runs "out-of-band, from a validator runner the
    // learner cannot reach." The environment may already be destroyed by
    // the time evaluation runs (doc §4.1: destroy happens after
    // evaluation completes in the happy path, but a suspended/reaped
    // attempt could reach here with environment_id already null) --
    // Phase 1's FakeValidatorExecutor tolerates a null/stale id since it
    // doesn't actually dial out to anything; a real executor will need
    // to re-provision or fail ERROR-not-FAIL per §6.2's guidance that a
    // platform-caused failure must never penalise the learner.
    const environmentId = attempt.environment_id ?? 'unknown-environment';

    const taskSummaries = await this.validatorRunner.run({
      attemptId,
      environmentId,
      scope: 'all',
      trigger: 'submit',
      tasks: spec.tasks,
    });

    // Doc §4.1 layer 2: "attempt_task_state (materialised per-task
    // status, updated by validator results) -- this is what the UI
    // reads." ValidatorRunnerService writes attempt_events (TASK_PASSED/
    // TASK_FAILED) but not the materialised table directly -- rebuilding
    // it from the event log here (rather than writing it inline in the
    // runner) keeps ReplayService as the single writer of that table, so
    // "rebuild from events" stays true under every code path, not just
    // the manual-recovery one.
    await this.replay.rebuildForAttempt(attemptId);

    const learnerActivityState = await this.db
      .selectFrom('learner.learner_activity_state')
      .selectAll()
      .where('user_id', '=', attempt.user_id)
      .where('activity_id', '=', attempt.activity_id)
      .executeTakeFirst();
    const wasFirstTryPass = attempt.retry_index === 0;

    const activeMinutes =
      attempt.started_at && attempt.submitted_at
        ? (attempt.submitted_at.getTime() - attempt.started_at.getTime()) /
          60000
        : 0;

    const criterionInput: CriterionInput = {
      taskResults: taskSummaries.map((t) => ({
        taskKey: t.taskKey,
        required: t.required,
        passed: t.passed,
        validatorResults: t.results.map((r) => ({
          validatorId: r.validatorId,
          status: r.status,
          weight: r.weight,
          severity: r.severity,
        })),
      })),
      hintPenaltyTotal: Number(attempt.hint_penalty_total),
      resetCount: attempt.reset_count,
      retryIndex: attempt.retry_index,
      activeMinutes,
      estimatedMinutes: version.estimated_minutes ?? 0,
    };

    const scoreResult = this.scoringEngine.score(
      criterionInput,
      GUIDED_LAB_DEFAULT_PROFILE,
      {
        wasFirstTryPass,
      },
    );

    await this.db
      .insertInto('attempt.attempt_score')
      .values({
        attempt_id: attemptId,
        profile_version_id: GUIDED_LAB_DEFAULT_PROFILE.id,
        criterion_fn_versions_jsonb: scoreResult.criterionFnVersions,
        final_score: scoreResult.finalScore,
        passed: scoreResult.passed,
        breakdown_jsonb: scoreResult.criteria as never,
        penalties_jsonb: {
          ...scoreResult.penalties,
          ...scoreResult.bonuses,
        } as never,
      })
      .execute();

    await this.events.append({
      attemptId,
      actor: 'SYSTEM',
      type: 'EVALUATED',
      payload: {
        final_score: scoreResult.finalScore,
        passed: scoreResult.passed,
      },
    });

    // Doc §2.7: "evidence apportioned to skills by activity_skill.weight."
    const activitySkills = await this.db
      .selectFrom('content.activity_skill')
      .selectAll()
      .where('activity_version_id', '=', attempt.activity_version_id)
      .execute();

    for (const as of activitySkills) {
      await this.mastery.recordEvidence({
        userId: attempt.user_id,
        skillId: as.skill_id,
        attemptId,
        score: scoreResult.finalScore,
        weight: Number(as.weight),
        passThreshold: GUIDED_LAB_DEFAULT_PROFILE.passThreshold,
        difficultyAdjust: 0, // Elo-based adjustment is §2.6, not yet wired to learner_elo
        wasGenuineAttempt: attempt.assistance_flags.length === 0,
      });
    }

    const nextStatus = scoreResult.passed ? 'PASSED' : 'FAILED';
    await this.db
      .updateTable('attempt.attempt')
      .set({
        status: nextStatus,
        completed_at: new Date(),
        version: attempt.version + 1,
      })
      .where('id', '=', attemptId)
      .where('version', '=', attempt.version)
      .execute();

    await this.db
      .insertInto('learner.learner_activity_state')
      .values({
        user_id: attempt.user_id,
        activity_id: attempt.activity_id,
        best_score: scoreResult.finalScore,
        latest_score: scoreResult.finalScore,
        status: nextStatus.toLowerCase(),
        attempts_count: 1,
        last_attempt_at: new Date(),
      })
      .onConflict((oc) =>
        oc.columns(['user_id', 'activity_id']).doUpdateSet({
          best_score: sql`GREATEST(learner.learner_activity_state.best_score, ${scoreResult.finalScore})`,
          latest_score: scoreResult.finalScore,
          status: nextStatus.toLowerCase(),
          attempts_count: sql`learner.learner_activity_state.attempts_count + 1`,
          last_attempt_at: new Date(),
        }),
      )
      .execute();

    this.logger.log(
      `Evaluated attempt ${attemptId}: score=${scoreResult.finalScore.toFixed(3)} passed=${scoreResult.passed}`,
    );

    return { finalScore: scoreResult.finalScore, passed: scoreResult.passed };
  }
}
