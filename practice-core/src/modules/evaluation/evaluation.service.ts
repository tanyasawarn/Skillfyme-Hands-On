import { Inject, Injectable, Logger } from '@nestjs/common';
import { Kysely, sql } from 'kysely';
import { KYSELY } from '../../db/database.module';
import type { Database } from '../../db/schema';
import { appendTypedEvent } from '../event-store/attempt-event-type';
import { EventStoreRepository } from '../event-store/event-store.repository';
import { ReplayService } from '../event-store/replay.service';
import { MasteryService } from '../skill/mastery.service';
import {
  ValidatorRunnerService,
  type TaskSpec,
} from './validator-runner.service';
import { ScoringEngineService } from './scoring-engine.service';
import {
  GUIDED_LAB_DEFAULT_PROFILE,
  PRODUCTION_SIM_DEFAULT_PROFILE,
  type ScoringProfile,
} from './scoring-profile';
import type { CriterionInput } from './criteria';
import { computeCooldownUntil } from './cooldown';
import type { ActivitySpec } from '../catalog/activity-spec';
import { FaultRepository } from './fault.repository';
import { ArtifactService } from './artifact.service';
import { AttemptRepository } from '../attempt/attempt.repository';

/**
 * Doc §6.4's profile-per-mode table (GUIDED_LAB -> sp.guided-lab.default,
 * PRODUCTION_SIM -> sp.production-sim.default, PROJECT ->
 * sp.project.default -- the last not yet built, Phase 3 scope). Every
 * activity YAML already authors scoring.profile (confirmed across all
 * 59 published activities); this registry is what makes evaluate()
 * actually honor that field instead of hardcoding
 * GUIDED_LAB_DEFAULT_PROFILE for every attempt regardless of mode, which
 * is what it did before sp.production-sim.default existed to select.
 */
const SCORING_PROFILE_REGISTRY: Record<string, ScoringProfile> = {
  [GUIDED_LAB_DEFAULT_PROFILE.id]: GUIDED_LAB_DEFAULT_PROFILE,
  [PRODUCTION_SIM_DEFAULT_PROFILE.id]: PRODUCTION_SIM_DEFAULT_PROFILE,
};

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
    private readonly faults: FaultRepository,
    private readonly artifacts: ArtifactService,
    private readonly attempts: AttemptRepository,
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

    // TaskSpec/ValidatorSpec (validator-runner.service.ts) are a
    // deliberately narrower internal-execution-input shape, not another
    // instance of K10's duplication -- ValidatorSpec.expect is required
    // (matches the schema) but ValidatorSpec.timeoutMs is camelCase
    // runtime-execution config, distinct from ActivitySpecValidator's
    // snake_case timeout_ms wire field, so the two types can't be
    // unified without conflating a content-schema field with an
    // execution-layer one.
    const specRaw = version.spec_jsonb as Pick<
      ActivitySpec,
      'environment' | 'faults' | 'scoring' | 'artifacts_required'
    > & { tasks?: TaskSpec[] };
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
      faults: specRaw.faults ?? [],
    };

    // Doc §6.4: every activity authors scoring.profile (confirmed across
    // all published content) -- select the matching profile rather than
    // hardcoding GUIDED_LAB_DEFAULT_PROFILE for every attempt regardless
    // of mode. An unrecognised/missing profile id falls back to
    // guided-lab's rather than throwing: a content authoring gap (typo'd
    // profile id, or a profile genuinely not built yet, e.g.
    // sp.project.default) must not crash evaluation for the learner.
    const profileId = specRaw.scoring?.profile;
    const profile =
      (profileId && SCORING_PROFILE_REGISTRY[profileId]) ||
      GUIDED_LAB_DEFAULT_PROFILE;
    if (profileId && !SCORING_PROFILE_REGISTRY[profileId]) {
      this.logger.warn(
        `attempt ${attemptId}: activity_version ${attempt.activity_version_id} references unknown scoring profile "${profileId}", falling back to ${GUIDED_LAB_DEFAULT_PROFILE.id}`,
      );
    }

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

    // Doc §6.4 efficiency gate: a validation run the learner triggered
    // themselves (manual "check my work," or auto_debounce) before this
    // evaluate() call's own submit-triggered run is evidence they
    // actually engaged with the task, not just that time passed. The
    // run this call just made above (ValidatorRunnerService.run with
    // trigger: 'submit', which inserts its own attempt.validation_run
    // row) is excluded by the trigger filter, not by ordering, so this
    // is correct regardless of when it was inserted relative to this
    // query.
    const preSubmitValidationRuns = await this.db
      .selectFrom('attempt.validation_run')
      .select((eb) => eb.fn.countAll().as('count'))
      .where('attempt_id', '=', attemptId)
      .where('trigger', '!=', 'submit')
      .executeTakeFirst();
    const preSubmitValidationRunCount = Number(
      preSubmitValidationRuns?.count ?? 0,
    );

    // Doc §6.4: "scoring reads signals, never raw events." Populated by
    // CommandExecutedConsumer as COMMAND_EXECUTED events arrive
    // throughout the attempt (see attempt.attempt_signal's own doc
    // comment in the migration) -- 0 for any signal_key with no rows,
    // which is the correct default for GUIDED_LAB attempts (no
    // process_signals authored, so nothing was ever checked or
    // incremented).
    const signalRows = await this.db
      .selectFrom('attempt.attempt_signal')
      .select(['signal_key', 'value_num'])
      .where('attempt_id', '=', attemptId)
      .execute();
    const signalValue = (key: string) =>
      Number(signalRows.find((r) => r.signal_key === key)?.value_num ?? 0);

    // Doc line 2047 "hypothesis_ordering": the learner's real command
    // sequence (attempt_events, doc §4.2's append-only log, already
    // seq-ordered by EventStoreRepository.replay) and each applied
    // fault's canonical_diagnostic_path (FaultRepository reads
    // content/faults/*.yaml -- see that repository's own doc comment on
    // why there's no DB table for this content type). Both empty for a
    // GUIDED_LAB attempt (no faults declared).
    const commandSequence = (await this.events.replay(attemptId))
      .filter((e) => e.type === 'COMMAND_EXECUTED')
      .map((e) => (e.payload as { cmd?: string }).cmd)
      .filter((cmd): cmd is string => !!cmd);
    const canonicalDiagnosticPaths = spec.faults
      .map((f) => this.faults.getCanonicalDiagnosticPath(f.id))
      .filter((path) => path.length > 0);

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
      preSubmitValidationRunCount,
      blastRadiusViolations: signalValue('blast_radius_violations'),
      diagnosticEfficiencyGoodCount: signalValue(
        'diagnostic_efficiency_good_count',
      ),
      diagnosticEfficiencyBadCount: signalValue(
        'diagnostic_efficiency_bad_count',
      ),
      commandSequence,
      canonicalDiagnosticPaths,
    };

    const scoreResult = this.scoringEngine.score(criterionInput, profile, {
      wasFirstTryPass,
    });

    await this.db
      .insertInto('attempt.attempt_score')
      .values({
        attempt_id: attemptId,
        profile_version_id: profile.id,
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

    await appendTypedEvent(this.events, {
      attemptId,
      actor: 'SYSTEM',
      type: 'EVALUATED',
      payload: {
        final_score: scoreResult.finalScore,
        passed: scoreResult.passed,
      },
    });

    // Doc §4.1 step 11: "AI portions run async; deterministic portion is
    // instant." The deterministic score above is already computed and
    // persisted -- AI grading of any required artifact runs after,
    // best-effort, and never affects finalScore/passed (doc rule 33's
    // fallback: "make that criterion advisory-only, weight zero, until
    // you can [calibrate]" -- exactly FakeAiGrader's current state, see
    // its own doc comment). A grading failure (missing rubric, no
    // artifact submitted, or a future real grader's own error) must not
    // fail the learner's whole evaluate() call over a side channel.
    for (const required of specRaw.artifacts_required ?? []) {
      try {
        await this.artifacts.gradeArtifact(attemptId, required.key);
      } catch (err) {
        this.logger.warn(
          `attempt ${attemptId}: AI grading failed for artifact "${required.key}": ${err instanceof Error ? err.message : err}`,
        );
      }
    }

    // Doc §2.6: "treat each attempt as a match" -- one learner-vs-activity
    // Elo match per attempt, anchored on the activity's primary skill
    // (content.activity_skill.is_primary), run before recordEvidence
    // below so its difficultyAdjust reflects pre-match ratings (see
    // recordEloMatch's own doc comment for why the ordering matters).
    // outcome mirrors doc §2.7's rule verbatim: 1 = first-try pass,
    // 0.5 = pass after retries, 0 = fail (an ABANDONED/EXPIRED attempt
    // never reaches evaluate() at all, so those outcomes don't apply
    // here).
    const primarySkillId = (
      await this.db
        .selectFrom('content.activity_skill')
        .select('skill_id')
        .where('activity_version_id', '=', attempt.activity_version_id)
        .where('is_primary', '=', true)
        .executeTakeFirst()
    )?.skill_id;

    let primarySkillDifficultyAdjust = 0;
    if (primarySkillId) {
      const eloOutcome = scoreResult.passed ? (wasFirstTryPass ? 1 : 0.5) : 0;
      const eloResult = await this.mastery.recordEloMatch({
        userId: attempt.user_id,
        skillId: primarySkillId,
        activityVersionId: attempt.activity_version_id,
        outcome: eloOutcome,
      });
      primarySkillDifficultyAdjust = eloResult.difficultyAdjust;
    }

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
        // Only the primary skill was the Elo match's anchor -- secondary
        // skills get no difficulty adjustment, same as before Elo existed.
        difficultyAdjust:
          as.skill_id === primarySkillId ? primarySkillDifficultyAdjust : 0,
        wasGenuineAttempt: attempt.assistance_flags.length === 0,
      });
    }

    // PLAN.md S7: this used to be a raw updateTable(...).where('version','=',...)
    // inline, which silently no-ops on a version mismatch instead of
    // surfacing the conflict -- reusing AttemptRepository.transition()
    // (the same optimistic-lock guard every other state transition in
    // this codebase already goes through, doc §4.4) makes a concurrent
    // writer here (e.g. the reaper force-destroying while evaluate() is
    // still running) raise ConflictException instead of silently
    // dropping this evaluation's result on the floor.
    const nextStatus = scoreResult.passed ? 'PASSED' : 'FAILED';
    await this.attempts.transition(attemptId, attempt.version, {
      status: nextStatus,
      completedAt: new Date(),
    });

    // Doc §2.7 retry cooldown: a fail sets cooldown_until so
    // EligibilityService blocks an immediate re-offer; a pass clears any
    // prior cooldown outright -- "retries should be earned by learning,"
    // and there's no reason to keep gating an activity the learner has
    // now actually passed.
    const cooldownUntil = scoreResult.passed
      ? null
      : computeCooldownUntil(attempt.retry_index);

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
        cooldown_until: cooldownUntil,
      })
      .onConflict((oc) =>
        oc.columns(['user_id', 'activity_id']).doUpdateSet({
          best_score: sql`GREATEST(learner.learner_activity_state.best_score, ${scoreResult.finalScore})`,
          latest_score: scoreResult.finalScore,
          status: nextStatus.toLowerCase(),
          attempts_count: sql`learner.learner_activity_state.attempts_count + 1`,
          last_attempt_at: new Date(),
          cooldown_until: cooldownUntil,
        }),
      )
      .execute();

    // Doc §2.7's early-clear clause: "cleared early if the learner
    // passes a remediation activity for the failed sub-skill." Only
    // matters on a pass (a fail can't clear anything), and only for
    // OTHER activities this one shares a primary skill with -- doc's
    // "remediation activity for the failed sub-skill" language means a
    // learner who passes activity B (targeting skill S) should have any
    // still-cooling-down activity A also targeting S unblocked, not
    // just A itself (A's own cooldown was already cleared above by the
    // scoreResult.passed ? null branch if A is what was just attempted).
    // Deliberately scoped to primary skills only (ask.is_primary), same
    // scope the recommendation engine's own remediation-candidate query
    // uses (recommendation.service.ts) -- a secondary/incidental skill
    // shared between two activities isn't the "sub-skill this activity
    // is remediating" doc §2.7 means.
    if (scoreResult.passed) {
      await this.clearCooldownsForRemediatedSkills(
        attempt.user_id,
        attempt.activity_id,
      );
    }

    this.logger.log(
      `Evaluated attempt ${attemptId}: score=${scoreResult.finalScore.toFixed(3)} passed=${scoreResult.passed}`,
    );

    return { finalScore: scoreResult.finalScore, passed: scoreResult.passed };
  }

  // Doc §2.7 early-clear: finds every OTHER activity currently in
  // cooldown for this learner that shares a primary skill with the
  // activity they just passed, and clears cooldown_until on all of
  // them. "Other" (activity_id != passedActivityId) matters: the just-
  // passed activity's own cooldown state was already handled by the
  // scoreResult.passed ? null : ... branch above this call site, so
  // re-touching its row here would be redundant, not wrong, but this
  // keeps the two code paths' responsibilities cleanly separated (one
  // owns "this activity's own state," the other owns "sibling
  // activities' cooldowns").
  private async clearCooldownsForRemediatedSkills(
    userId: string,
    passedActivityId: string,
  ): Promise<void> {
    const remediatedSkillIds = await this.db
      .selectFrom('content.activity_skill as ask')
      .innerJoin(
        'content.activity_version as av',
        'av.id',
        'ask.activity_version_id',
      )
      .select('ask.skill_id')
      .where('av.activity_id', '=', passedActivityId)
      .where('ask.is_primary', '=', true)
      .distinct()
      .execute();

    if (remediatedSkillIds.length === 0) return;
    const skillIds = remediatedSkillIds.map((r) => r.skill_id);

    const activitiesTargetingSameSkills = await this.db
      .selectFrom('content.activity_skill as ask')
      .innerJoin(
        'content.activity_version as av',
        'av.id',
        'ask.activity_version_id',
      )
      .select('av.activity_id')
      .distinct()
      .where('ask.skill_id', 'in', skillIds)
      .where('ask.is_primary', '=', true)
      .where('av.activity_id', '!=', passedActivityId)
      .execute();

    if (activitiesTargetingSameSkills.length === 0) return;
    const candidateActivityIds = activitiesTargetingSameSkills.map(
      (r) => r.activity_id,
    );

    const result = await this.db
      .updateTable('learner.learner_activity_state')
      .set({ cooldown_until: null })
      .where('user_id', '=', userId)
      .where('activity_id', 'in', candidateActivityIds)
      .where('cooldown_until', 'is not', null)
      .executeTakeFirst();

    const clearedCount = Number(result.numUpdatedRows ?? 0);
    if (clearedCount > 0) {
      this.logger.log(
        `Early-cleared cooldown on ${clearedCount} activit${clearedCount === 1 ? 'y' : 'ies'} for user ${userId} after passing ${passedActivityId} (doc §2.7 remediation early-clear, skills=[${skillIds.join(',')}])`,
      );
    }
  }
}
