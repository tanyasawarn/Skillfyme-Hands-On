import {
  BadRequestException,
  Inject,
  Injectable,
  Logger,
} from '@nestjs/common';
import { createHash } from 'node:crypto';
import type { Kysely } from 'kysely';
import { KYSELY } from '../../db/database.module';
import type { Database } from '../../db/schema';
import { appendTypedEvent } from '../event-store/attempt-event-type';
import { EventStoreRepository } from '../event-store/event-store.repository';
import { RubricRepository } from './rubric.repository';
import { AI_GRADER, type AiGrader } from './ai-grader.interface';
import type { ActivitySpec } from '../catalog/activity-spec';
import { findOrThrow } from '../../common/find-or-throw';
import { ActivitySpecReader } from '../../common/activity-spec-reader';

/**
 * Doc §6.5/§1.3.2 step 9: the incident-note (and any other
 * artifacts_required entry) submission + AI-grading pipeline. Doc rule
 * 34's "deterministic pre-processing" is this service's core job: it
 * assembles GradingFacts from the attempt's own real state (applied
 * faults, resolution validator results, command history) before ever
 * calling the grader, rather than letting the grader see a raw
 * environment.
 */
@Injectable()
export class ArtifactService {
  private readonly logger = new Logger(ArtifactService.name);

  constructor(
    @Inject(KYSELY) private readonly db: Kysely<Database>,
    private readonly events: EventStoreRepository,
    private readonly rubrics: RubricRepository,
    @Inject(AI_GRADER) private readonly grader: AiGrader,
    private readonly specReader: ActivitySpecReader,
  ) {}

  /**
   * Doc §3.3 worked example: artifacts_required: [{key, type, rubric}].
   * Validates the submitted key is one the activity actually requires
   * before accepting content -- an arbitrary key would have no rubric to
   * grade against and nothing to attach it to on the evaluation side.
   */
  async submit(
    attemptId: string,
    key: string,
    content: string,
  ): Promise<{ id: string }> {
    const spec = findOrThrow(
      await this.specReader.getActivitySpec(attemptId),
      `attempt ${attemptId} not found`,
    ) as Pick<ActivitySpec, 'artifacts_required'>;
    const required = (spec.artifacts_required ?? []).find((a) => a.key === key);
    if (!required) {
      throw new BadRequestException(
        `activity does not require an artifact with key "${key}"`,
      );
    }

    const checksum = createHash('sha256').update(content).digest('hex');
    const row = await this.db
      .insertInto('attempt.artifact')
      .values({
        attempt_id: attemptId,
        kind: key,
        storage_uri: 'local://inline', // see migration 0006's doc comment
        content,
        bytes: Buffer.byteLength(content, 'utf-8'),
        checksum,
      })
      .returningAll()
      .executeTakeFirstOrThrow();

    await appendTypedEvent(this.events, {
      attemptId,
      actor: 'LEARNER',
      type: 'EDITOR_SAVE', // closest existing taxonomy entry for "learner submitted written content" -- see contracts/events.md
      payload: { artifact_key: key, bytes: row.bytes, checksum },
    });

    return { id: row.id };
  }

  /**
   * Doc rule 34's deterministic pre-processing + rule 32's structured
   * output, run for one submitted artifact against its required rubric.
   * Returns null if the activity requires no artifact with this key, or
   * the rubric file doesn't exist (a content-authoring gap, not a
   * learner-facing error -- evaluate() must not crash over it).
   */
  async gradeArtifact(
    attemptId: string,
    key: string,
  ): Promise<{
    rubricId: string;
    provisional: boolean;
    criterionGrades: unknown[];
  } | null> {
    const artifact = await this.db
      .selectFrom('attempt.artifact')
      .selectAll()
      .where('attempt_id', '=', attemptId)
      .where('kind', '=', key)
      .orderBy('created_at', 'desc')
      .executeTakeFirst();
    if (!artifact || artifact.content === null) return null;

    // Unlike submit()'s findOrThrow, a missing attempt here throws via
    // this call site's caller-side try/catch (evaluate(), which already
    // treats "grading failed" as expected-and-logged, never learner-
    // facing) -- findOrThrow's real difference from the original
    // .executeTakeFirstOrThrow() is only in the exact error type/message,
    // both still throw and get caught the same way.
    const spec = findOrThrow(
      await this.specReader.getActivitySpec(attemptId),
      `attempt ${attemptId} not found`,
    ) as Pick<ActivitySpec, 'artifacts_required' | 'faults'>;
    const required = (spec.artifacts_required ?? []).find((a) => a.key === key);
    if (!required?.rubric) return null;

    const rubric = this.rubrics.getRubric(required.rubric);
    if (!rubric) {
      this.logger.warn(
        `attempt ${attemptId}: rubric "${required.rubric}" not found, skipping AI grading`,
      );
      return null;
    }

    // Doc rule 34 / §1.3.2 step 9: ground truth, not raw environment access.
    const commandSequence = (await this.events.replay(attemptId))
      .filter((e) => e.type === 'COMMAND_EXECUTED')
      .map((e) => (e.payload as { cmd?: string }).cmd)
      .filter((cmd): cmd is string => !!cmd);
    const validatorResults = await this.db
      .selectFrom('attempt.validator_result as vr')
      .innerJoin(
        'attempt.validation_run as run',
        'run.id',
        'vr.validation_run_id',
      )
      .select(['vr.validator_id', 'vr.status'])
      .where('run.attempt_id', '=', attemptId)
      .execute();

    const result = await this.grader.grade(rubric, {
      artifactText: artifact.content,
      appliedFaultIds: (spec.faults ?? []).map((f) => f.id),
      resolutionValidatorResults: validatorResults.map((r) => ({
        validatorId: r.validator_id,
        status: r.status,
      })),
      commandSequence,
    });

    await appendTypedEvent(this.events, {
      attemptId,
      actor: 'AI',
      type: 'AI_MESSAGE',
      payload: {
        rubric_id: rubric.id,
        provisional: result.provisional,
        grades: result.criterionGrades,
      },
    });

    return {
      rubricId: result.rubricId,
      provisional: result.provisional,
      criterionGrades: result.criterionGrades,
    };
  }
}
