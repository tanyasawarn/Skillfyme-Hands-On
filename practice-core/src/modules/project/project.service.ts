import { Inject, Injectable, Logger } from '@nestjs/common';
import type { Kysely } from 'kysely';
import { KYSELY } from '../../db/database.module';
import type { Database, ProjectMilestoneKey } from '../../db/schema';
import { findOrThrow } from '../../common/find-or-throw';
import { appendTypedEvent } from '../event-store/attempt-event-type';
import { EventStoreRepository } from '../event-store/event-store.repository';
import { RubricRepository } from '../evaluation/rubric.repository';
import { AI_GRADER, type AiGrader } from '../evaluation/ai-grader.interface';
import {
  ValidatorRunnerService,
  type TaskSpec,
} from '../evaluation/validator-runner.service';
import type { ActivitySpec } from '../catalog/activity-spec';
import { ProjectRepository } from './project.repository';
import { GitService } from './git.service';
import { DefenceService } from './defence.service';
import {
  ProjectScoringService,
  SP_PROJECT_DEFAULT_ID,
} from './project-scoring';
import {
  PROJECT_ORCHESTRATOR,
  type ProjectOrchestratorPort,
} from './project-orchestrator.port';
import { projectError } from './project-error';

/**
 * Phase 3 (PLAN_PHASE3_PROJECTS.md 1.6 / B2). The project-mode milestone
 * state machine.
 *
 * Per attempt: an ordered milestone sequence design → infra →
 * implementation → hardening → final. Each milestone has
 * status ∈ {LOCKED, OPEN, SUBMITTED, GATED_PASS, GATED_FAIL}. On :submit
 * a milestone's validators (B3) + rubric slice (B5) run; a blocking
 * milestone that does not clear its gate keeps the next one LOCKED.
 *
 * Environment lifecycle (§12.3): milestone `design` needs no environment;
 * `infra`…`hardening` provision a T3 env on demand (via the fake port
 * until Stage 3.2) and snapshot-suspend it on idle; `final` runs the full
 * acceptance suite.
 *
 * The gate for `design` is the hard one — RUBRIC_MIN_LEVEL on
 * rub.architecture.v3's `overall` criterion at level 3/5 (memory.md
 * §12.3). Its score is provisional until the calibration harness passes
 * (1.9 / rub-calibration.md), so a GATED_PASS on design carries a
 * `provisional` flag the human-review queue picks up.
 */

export const MILESTONE_SEQUENCE: readonly ProjectMilestoneKey[] = [
  'design',
  'infra',
  'implementation',
  'hardening',
  'final',
] as const;

interface MilestoneSpec {
  key: ProjectMilestoneKey;
  title: string;
  gate: 'ALL_VALIDATORS_PASS' | 'RUBRIC_MIN_LEVEL' | 'BOTH';
  blocking: boolean;
  environmentRequired: boolean;
  taskKeys: string[];
  rubric?: string;
  minLevel?: number;
}

export interface MilestoneView {
  key: ProjectMilestoneKey;
  title: string;
  status: string;
  ordinal: number;
  gate: string;
  blocking: boolean;
  environmentRequired: boolean;
  score: number | null;
  rubricLevel: number | null;
  attemptCount: number;
  submittedAt: Date | null;
  gatedAt: Date | null;
}

export interface SubmitMilestoneResult {
  milestoneKey: ProjectMilestoneKey;
  outcome: 'GATED_PASS' | 'GATED_FAIL';
  score: number;
  rubricLevel?: number;
  provisional: boolean;
  nextMilestoneOpened: ProjectMilestoneKey | null;
  validatorSummary: Array<{ taskKey: string; passed: boolean }>;
}

@Injectable()
export class ProjectService {
  private readonly logger = new Logger(ProjectService.name);

  constructor(
    @Inject(KYSELY) private readonly db: Kysely<Database>,
    private readonly projects: ProjectRepository,
    private readonly events: EventStoreRepository,
    private readonly validatorRunner: ValidatorRunnerService,
    private readonly rubrics: RubricRepository,
    @Inject(AI_GRADER) private readonly grader: AiGrader,
    private readonly git: GitService,
    private readonly scoring: ProjectScoringService,
    private readonly defence: DefenceService,
    @Inject(PROJECT_ORCHESTRATOR)
    private readonly orchestrator: ProjectOrchestratorPort,
  ) {}

  /**
   * Seed the milestone rows for a fresh project attempt from the
   * activity spec's `milestones[]`. First milestone OPEN, rest LOCKED.
   * Idempotent (ProjectRepository.seedMilestones uses ON CONFLICT).
   */
  async initProjectAttempt(attemptId: string): Promise<MilestoneView[]> {
    const specs = await this.loadMilestoneSpecs(attemptId);
    await this.projects.seedMilestones(
      attemptId,
      specs.map((m, i) => ({
        milestoneKey: m.key,
        ordinal: i,
        status: i === 0 ? 'OPEN' : 'LOCKED',
      })),
    );
    await appendTypedEvent(this.events, {
      attemptId,
      actor: 'SYSTEM',
      type: 'MILESTONE_SUBMITTED', // reuse the taxonomy 'seeded' marker isn't a type; emit on first list instead
      payload: { seeded: specs.map((m) => m.key) },
    }).catch(() => undefined);
    return this.listMilestones(attemptId);
  }

  async listMilestones(attemptId: string): Promise<MilestoneView[]> {
    const specs = await this.loadMilestoneSpecs(attemptId);
    const rows = await this.projects.listMilestones(attemptId);
    if (rows.length === 0) {
      // not seeded yet — seed lazily so GET is safe to call first
      await this.initProjectAttempt(attemptId);
      return this.listMilestones(attemptId);
    }
    const specByKey = new Map(specs.map((s) => [s.key, s]));
    return rows.map((r) => {
      const s = specByKey.get(r.milestone_key);
      return {
        key: r.milestone_key,
        title: s?.title ?? r.milestone_key,
        status: r.status,
        ordinal: r.ordinal,
        gate: s?.gate ?? 'ALL_VALIDATORS_PASS',
        blocking: s?.blocking ?? true,
        environmentRequired:
          s?.environmentRequired ?? r.milestone_key !== 'design',
        score: r.score,
        rubricLevel: r.rubric_level,
        attemptCount: r.attempt_count,
        submittedAt: r.submitted_at,
        gatedAt: r.gated_at,
      };
    });
  }

  /**
   * Submit a milestone for gating. Runs its validator set + rubric slice,
   * applies the gate outcome (opening the next milestone on GATED_PASS of
   * a blocking milestone), records the submission, and emits
   * MILESTONE_GATED.
   */
  async submitMilestone(input: {
    attemptId: string;
    milestoneKey: ProjectMilestoneKey;
    /** the learner's design doc text (design milestone) or omitted for infra+ */
    designText?: string;
    /** commit the learner declares as the submission point; HEAD is resolved otherwise */
    commitSha?: string;
  }): Promise<SubmitMilestoneResult> {
    const { attemptId, milestoneKey } = input;
    const specs = await this.loadMilestoneSpecs(attemptId);
    const spec = findOrThrow(
      specs.find((s) => s.key === milestoneKey),
      `activity has no milestone "${milestoneKey}"`,
    );

    const rows = await this.listMilestones(attemptId);
    const row = findOrThrow(
      rows.find((r) => r.key === milestoneKey),
      `milestone "${milestoneKey}" not found for attempt ${attemptId}`,
    );
    if (row.status === 'LOCKED') {
      projectError('MILESTONE_LOCKED', `milestone "${milestoneKey}" is LOCKED`);
    }
    if (row.status === 'GATED_PASS') {
      projectError(
        'MILESTONE_ALREADY_PASSED',
        `milestone "${milestoneKey}" has already passed`,
      );
    }

    const attemptNumber = await this.projects.markSubmitted(
      attemptId,
      milestoneKey,
    );

    // record the submission pointer (repo_ref + commit_sha)
    const attempt = await this.db
      .selectFrom('attempt.attempt')
      .select(['id'])
      .where('id', '=', attemptId)
      .executeTakeFirst();
    void attempt;
    const repoRef = await this.resolveRepoRef(attemptId);
    let commitSha = input.commitSha ?? '';
    if (repoRef) {
      const rec = await this.git.recordMilestoneSubmission({
        attemptId,
        milestoneKey,
        repoRef,
        attemptNumber,
        commitSha: input.commitSha,
      });
      commitSha = rec.commitSha;
    } else {
      await this.projects.recordSubmission({
        attemptId,
        milestoneKey,
        repoRef: 'none',
        commitSha,
        attemptNumber,
      });
    }

    await appendTypedEvent(this.events, {
      attemptId,
      actor: 'LEARNER',
      type: 'MILESTONE_SUBMITTED',
      payload: { milestone_key: milestoneKey, attempt_number: attemptNumber },
    });

    // --- run the gate ---------------------------------------------------
    const validatorSummary = await this.runMilestoneValidators(attemptId, spec);
    const validatorsPass =
      spec.taskKeys.length === 0 || validatorSummary.every((t) => t.passed);

    let rubricLevel: number | undefined;
    let rubricPass = true;
    let provisional = false;
    if (spec.gate === 'RUBRIC_MIN_LEVEL' || spec.gate === 'BOTH') {
      const graded = await this.gradeMilestoneRubric(
        spec,
        input.designText ?? '',
      );
      rubricLevel = graded.gateLevel;
      rubricPass = graded.gateLevel >= (spec.minLevel ?? 3);
      provisional = graded.provisional;
    }

    const passed =
      spec.gate === 'ALL_VALIDATORS_PASS'
        ? validatorsPass
        : spec.gate === 'RUBRIC_MIN_LEVEL'
          ? rubricPass
          : validatorsPass && rubricPass;

    const score = this.computeMilestoneScore(
      spec,
      validatorSummary,
      rubricLevel,
    );
    const outcome: 'GATED_PASS' | 'GATED_FAIL' = passed
      ? 'GATED_PASS'
      : 'GATED_FAIL';

    await this.projects.applyGateOutcome({
      attemptId,
      milestoneKey,
      outcome,
      score,
      rubricLevel,
    });
    await this.projects.stampSubmissionOutcome(
      attemptId,
      milestoneKey,
      attemptNumber,
      outcome,
      score,
    );

    // provision / suspend the environment per lifecycle rules
    await this.applyEnvLifecycle(attemptId, spec, outcome);

    const nextOpened = await this.nextOpenedMilestone(
      attemptId,
      milestoneKey,
      outcome,
      spec.blocking,
    );

    await appendTypedEvent(this.events, {
      attemptId,
      actor: 'SYSTEM',
      type: 'MILESTONE_GATED',
      payload: {
        milestone_key: milestoneKey,
        outcome,
        score,
        ...(rubricLevel !== undefined ? { rubric_level: rubricLevel } : {}),
        provisional,
      },
    });

    // Stage 3.9: when the last milestone gates, roll up the whole project
    // score (sp.project.default) and persist attempt_score.
    if (milestoneKey === 'final' && outcome === 'GATED_PASS') {
      await this.finalizeProjectScore(attemptId).catch((e: unknown) =>
        this.logger.error(
          `project score roll-up failed for ${attemptId}: ${
            e instanceof Error ? e.message : String(e)
          }`,
        ),
      );
    }

    return {
      milestoneKey,
      outcome,
      score,
      rubricLevel,
      provisional,
      nextMilestoneOpened: nextOpened,
      validatorSummary,
    };
  }

  /**
   * Stage 3.9. Roll up the five milestone scores + the defence viva score
   * into the final project score via ProjectScoringService, persist
   * attempt_score under sp.project.default, and emit EVALUATED. Mastery
   * update (BKT per mapped skill) is left to the existing evaluation
   * path — this method surfaces the per-skill evidence in the event so
   * that path (or a future project-specific mastery updater) can consume
   * it.
   */
  private async finalizeProjectScore(attemptId: string): Promise<void> {
    const milestones = await this.listMilestones(attemptId);
    const specs = await this.loadMilestoneSpecs(attemptId);
    const specByKey = new Map(specs.map((s) => [s.key, s]));

    const defenceScore = await this.defence.defenceComponentScore(attemptId);

    const mappedSkills = await this.db
      .selectFrom('attempt.attempt as a')
      .innerJoin(
        'content.activity_skill as sk',
        'sk.activity_version_id',
        'a.activity_version_id',
      )
      .select(['sk.skill_id as skillId', 'sk.weight as weight'])
      .where('a.id', '=', attemptId)
      .execute();

    const rollup = this.scoring.rollup({
      milestones: milestones.map((m) => ({
        key: m.key,
        score: m.score,
        aiDerived:
          specByKey.get(m.key)?.gate === 'RUBRIC_MIN_LEVEL' ||
          specByKey.get(m.key)?.gate === 'BOTH',
      })),
      defenceScore,
      mappedSkills: mappedSkills.map((s) => ({
        skillId: String(s.skillId),
        weight: Number(s.weight),
      })),
    });

    await this.db
      .insertInto('attempt.attempt_score')
      .values({
        attempt_id: attemptId,
        profile_version_id: SP_PROJECT_DEFAULT_ID,
        criterion_fn_versions_jsonb: { 'sp.project.default': 1 } as never,
        final_score: rollup.finalScore,
        passed: rollup.passed,
        breakdown_jsonb: {
          components: rollup.breakdown,
          ai_fraction: rollup.aiFraction,
          ai_cap_applied: rollup.aiCapApplied,
        } as never,
        penalties_jsonb: {} as never,
      })
      .execute();

    await appendTypedEvent(this.events, {
      attemptId,
      actor: 'SYSTEM',
      type: 'EVALUATED',
      payload: {
        final_score: rollup.finalScore,
        passed: rollup.passed,
        profile: SP_PROJECT_DEFAULT_ID,
        ai_fraction: rollup.aiFraction,
        mastery_evidence: rollup.masteryEvidence,
      },
    });
    this.logger.log(
      `project ${attemptId} finalized: score=${rollup.finalScore} passed=${rollup.passed} (ai=${rollup.aiFraction})`,
    );
  }

  // --- helpers -------------------------------------------------------------

  private async loadMilestoneSpecs(
    attemptId: string,
  ): Promise<MilestoneSpec[]> {
    const version = await this.db
      .selectFrom('attempt.attempt as a')
      .innerJoin(
        'content.activity_version as v',
        'v.id',
        'a.activity_version_id',
      )
      .select(['v.spec_jsonb', 'a.mode'])
      .where('a.id', '=', attemptId)
      .executeTakeFirst();
    if (!version) {
      projectError('ATTEMPT_NOT_FOUND', `attempt ${attemptId} not found`);
    }
    if (version.mode !== 'PROJECT') {
      projectError(
        'NOT_A_PROJECT',
        `attempt ${attemptId} is mode ${version.mode}, not PROJECT`,
      );
    }
    const spec = version.spec_jsonb as Pick<ActivitySpec, 'milestones'>;
    const milestones = spec.milestones ?? [];
    if (milestones.length === 0) {
      // Spec has no milestones[] — fall back to the canonical 5-stage
      // sequence with validator-only gates and the design hard-gate, so
      // the state machine is exercisable before content authors add the
      // section. A real published PROJECT activity always authors it.
      return MILESTONE_SEQUENCE.map((key) => ({
        key,
        title: key[0].toUpperCase() + key.slice(1),
        gate: key === 'design' ? 'RUBRIC_MIN_LEVEL' : 'ALL_VALIDATORS_PASS',
        blocking: true,
        environmentRequired: key !== 'design',
        taskKeys: [],
        rubric: key === 'design' ? 'rub.architecture.v3' : undefined,
        minLevel: key === 'design' ? 3 : undefined,
      }));
    }
    return milestones.map((m) => ({
      key: m.key,
      title: m.title,
      gate: m.gate,
      blocking: m.blocking ?? true,
      environmentRequired: m.environment_required,
      taskKeys: m.task_keys ?? [],
      rubric: m.rubric,
      minLevel: m.min_level,
    }));
  }

  private async runMilestoneValidators(
    attemptId: string,
    spec: MilestoneSpec,
  ): Promise<Array<{ taskKey: string; passed: boolean }>> {
    if (spec.taskKeys.length === 0) return [];
    const version = await this.db
      .selectFrom('attempt.attempt as a')
      .innerJoin(
        'content.activity_version as v',
        'v.id',
        'a.activity_version_id',
      )
      .select(['v.spec_jsonb'])
      .where('a.id', '=', attemptId)
      .executeTakeFirstOrThrow();
    const full = version.spec_jsonb as { tasks?: TaskSpec[] };
    const tasks = (full.tasks ?? []).filter((t) =>
      spec.taskKeys.includes(t.key),
    );
    const attempt = await this.db
      .selectFrom('attempt.attempt')
      .select(['environment_id'])
      .where('id', '=', attemptId)
      .executeTakeFirstOrThrow();

    const summaries = await this.validatorRunner.run({
      attemptId,
      environmentId: attempt.environment_id ?? 'unknown-environment',
      scope: 'all',
      trigger: 'submit',
      tasks,
    });
    return summaries.map((s) => ({ taskKey: s.taskKey, passed: s.passed }));
  }

  private async gradeMilestoneRubric(
    spec: MilestoneSpec,
    designText: string,
  ): Promise<{ gateLevel: number; provisional: boolean }> {
    if (!spec.rubric) return { gateLevel: 0, provisional: true };
    const rubric = this.rubrics.getRubric(spec.rubric);
    if (!rubric) {
      this.logger.warn(
        `milestone ${spec.key}: rubric ${spec.rubric} not found — gate fails closed`,
      );
      return { gateLevel: 0, provisional: true };
    }
    const result = await this.grader.grade(rubric, {
      artifactText: designText,
      appliedFaultIds: [],
      resolutionValidatorResults: [],
      commandSequence: [],
    });
    // The gate criterion is `overall` if present, else the lowest level
    // across criteria (conservative).
    const overall = result.criterionGrades.find(
      (g) => g.criterion === 'overall',
    );
    const gateLevel = overall
      ? overall.level
      : Math.min(...result.criterionGrades.map((g) => g.level));
    return { gateLevel, provisional: result.provisional };
  }

  private computeMilestoneScore(
    spec: MilestoneSpec,
    validatorSummary: Array<{ taskKey: string; passed: boolean }>,
    rubricLevel?: number,
  ): number {
    const validatorScore =
      validatorSummary.length === 0
        ? 1
        : validatorSummary.filter((t) => t.passed).length /
          validatorSummary.length;
    const rubricScore =
      rubricLevel === undefined ? 1 : clamp01((rubricLevel - 1) / 3); // 1..4 → 0..1

    switch (spec.gate) {
      case 'ALL_VALIDATORS_PASS':
        return round4(validatorScore);
      case 'RUBRIC_MIN_LEVEL':
        return round4(rubricScore);
      default:
        return round4((validatorScore + rubricScore) / 2);
    }
  }

  private async applyEnvLifecycle(
    attemptId: string,
    spec: MilestoneSpec,
    outcome: 'GATED_PASS' | 'GATED_FAIL',
  ): Promise<void> {
    if (!spec.environmentRequired) return;
    const attempt = await this.db
      .selectFrom('attempt.attempt')
      .select(['environment_id'])
      .where('id', '=', attemptId)
      .executeTakeFirst();

    if (outcome === 'GATED_PASS') {
      // milestone passed → suspend the compute (project mode: suspension is the norm)
      if (attempt?.environment_id) {
        await this.orchestrator
          .snapshotAndSuspend({
            attemptId,
            environmentId: attempt.environment_id,
          })
          .catch((e: unknown) =>
            this.logger.warn(
              `snapshotAndSuspend failed for ${attemptId}: ${
                e instanceof Error ? e.message : String(e)
              }`,
            ),
          );
      }
      return;
    }

    // GATED_FAIL → keep an environment available so the learner can iterate
    if (!attempt?.environment_id) {
      const env = await this.orchestrator
        .provisionForMilestone({ attemptId, milestoneKey: spec.key })
        .catch((e: unknown) => {
          this.logger.warn(
            `provisionForMilestone failed for ${attemptId}: ${
              e instanceof Error ? e.message : String(e)
            }`,
          );
          return null;
        });
      if (env?.environmentId) {
        await this.db
          .updateTable('attempt.attempt')
          .set({ environment_id: env.environmentId })
          .where('id', '=', attemptId)
          .execute();
      }
    }
  }

  private async nextOpenedMilestone(
    attemptId: string,
    milestoneKey: ProjectMilestoneKey,
    outcome: 'GATED_PASS' | 'GATED_FAIL',
    blocking: boolean,
  ): Promise<ProjectMilestoneKey | null> {
    if (outcome !== 'GATED_PASS') return null;
    void blocking;
    const rows = await this.projects.listMilestones(attemptId);
    const cur = rows.find((r) => r.milestone_key === milestoneKey);
    if (!cur) return null;
    const next = rows.find((r) => r.ordinal === cur.ordinal + 1);
    // applyGateOutcome already opened it inside its transaction; just report.
    return next && next.status === 'OPEN' ? next.milestone_key : null;
  }

  private async resolveRepoRef(attemptId: string): Promise<string | null> {
    const prior = await this.projects.listSubmissions(attemptId);
    const withRepo = prior.find((s) => s.repo_ref && s.repo_ref !== 'none');
    return withRepo?.repo_ref ?? null;
  }
}

function clamp01(n: number): number {
  return Math.max(0, Math.min(1, n));
}
function round4(n: number): number {
  return Math.round(n * 1e4) / 1e4;
}
