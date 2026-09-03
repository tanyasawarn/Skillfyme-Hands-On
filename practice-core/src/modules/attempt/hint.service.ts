import { BadRequestException, Inject, Injectable } from '@nestjs/common';
import { sql, type Kysely } from 'kysely';
import { KYSELY } from '../../db/database.module';
import type { Database } from '../../db/schema';
import { appendTypedEvent } from '../event-store/attempt-event-type';
import { EventStoreRepository } from '../event-store/event-store.repository';
import { findOrThrow } from '../../common/find-or-throw';
import type { ActivitySpec, ActivitySpecTask } from '../catalog/activity-spec';
import { ActivitySpecReader } from '../../common/activity-spec-reader';

export interface HintPreview {
  taskKey: string;
  nextLevel: number;
  penalty: number;
  hasMoreAfterThis: boolean;
}

export interface HintReveal extends HintPreview {
  text: string;
}

export interface GuidedFallback {
  taskKey: string;
  /** The final authored guidance shown (the last hint level's text, or the
   *  task's solution_apply hint if no hints authored). */
  text: string;
  /** Always true here -- the whole point of the fallback is that the
   *  attempt is now assisted and this task earns zero positive BKT
   *  evidence (doc §7.5). */
  assisted: true;
}

/** Assistance-flag value written to attempt.assistance_flags when the
 *  learner takes the "just tell me" guided fallback. EvaluationService
 *  reads assistance_flags.length to set BktService.wasGenuineAttempt. */
export const GUIDED_FALLBACK_FLAG = 'guided_fallback';

/**
 * Doc §7.5 "just tell me" escalation contract (static-hint subset --
 * §7.5 as a whole assumes an AI mentor mediating the conversation, which
 * is Phase 4; Phase 1 has no mentor, so this is the authored-ladder half
 * of that contract only): "mentor offers the next hint level explicitly,
 * stating the score cost... Transparency converts an adversarial
 * interaction into an informed choice." preview() is side-effect-free
 * (no event, no penalty applied) so the UI can show the cost before the
 * learner commits; reveal() is the accept step doc step 39 describes:
 * "Learner accepts -> HINT_REQUESTED event -> hint delivered -> penalty
 * recorded."
 */
@Injectable()
export class HintService {
  constructor(
    @Inject(KYSELY) private readonly db: Kysely<Database>,
    private readonly events: EventStoreRepository,
    private readonly specReader: ActivitySpecReader,
  ) {}

  private async getTaskSpec(
    attemptId: string,
    taskKey: string,
  ): Promise<ActivitySpecTask> {
    const spec = findOrThrow(
      await this.specReader.getActivitySpec(attemptId),
      `attempt ${attemptId} not found`,
    ) as Pick<ActivitySpec, 'tasks'>;
    const task = spec.tasks?.find((t) => t.key === taskKey);
    if (!task)
      throw new BadRequestException(
        `task ${taskKey} not found on this activity`,
      );
    return task;
  }

  private async getUsedMaxLevel(
    attemptId: string,
    taskKey: string,
  ): Promise<number> {
    const row = await this.db
      .selectFrom('attempt.attempt_task_state')
      .select('hints_used_max_level')
      .where('attempt_id', '=', attemptId)
      .where('task_key', '=', taskKey)
      .executeTakeFirst();
    return row?.hints_used_max_level ?? 0;
  }

  /** No side effects -- doc §7.5 step 38: show the cost before the learner commits. */
  async preview(
    attemptId: string,
    taskKey: string,
  ): Promise<HintPreview | null> {
    const task = await this.getTaskSpec(attemptId, taskKey);
    const hints = (task.hints ?? []).sort((a, b) => a.level - b.level);
    const usedMaxLevel = await this.getUsedMaxLevel(attemptId, taskKey);

    const next = hints.find((h) => h.level > usedMaxLevel);
    if (!next) return null; // ladder exhausted -- doc step 40: caller should offer the guided fallback

    return {
      taskKey,
      nextLevel: next.level,
      penalty: next.penalty,
      hasMoreAfterThis: hints.some((h) => h.level > next.level),
    };
  }

  /** Doc §7.5 step 39: accept -> event -> reveal -> penalty recorded. */
  async reveal(attemptId: string, taskKey: string): Promise<HintReveal> {
    const task = await this.getTaskSpec(attemptId, taskKey);
    const hints = (task.hints ?? []).sort((a, b) => a.level - b.level);
    const usedMaxLevel = await this.getUsedMaxLevel(attemptId, taskKey);

    const next = hints.find((h) => h.level > usedMaxLevel);
    if (!next) {
      throw new BadRequestException(
        `hint ladder exhausted for task ${taskKey} (doc §7.5: offer the guided fallback instead)`,
      );
    }

    await appendTypedEvent(this.events, {
      attemptId,
      actor: 'LEARNER',
      type: 'HINT_REQUESTED',
      payload: { task: taskKey, level: next.level },
    });

    await this.db.transaction().execute(async (trx) => {
      await trx
        .insertInto('attempt.attempt_task_state')
        .values({
          attempt_id: attemptId,
          task_key: taskKey,
          hints_used_max_level: next.level,
        })
        .onConflict((oc) =>
          oc.columns(['attempt_id', 'task_key']).doUpdateSet({
            hints_used_max_level: next.level,
          }),
        )
        .execute();

      await trx
        .updateTable('attempt.attempt')
        .set({
          hint_penalty_total: sql`attempt.hint_penalty_total + ${next.penalty}`,
        })
        .where('id', '=', attemptId)
        .execute();
    });

    return {
      taskKey,
      nextLevel: next.level,
      penalty: next.penalty,
      hasMoreAfterThis: hints.some((h) => h.level > next.level),
      text: next.text,
    };
  }

  /**
   * Doc §7.5 step 40 -- the "just tell me" guided fallback. Reachable
   * only once the hint ladder is exhausted (every authored level already
   * revealed). Shows the final authored guidance and marks the attempt
   * ASSISTED: 'guided_fallback' is appended to attempt.assistance_flags,
   * which EvaluationService reads (assistance_flags.length === 0 ->
   * wasGenuineAttempt) so this task then contributes ZERO positive
   * evidence to BKT -- the learner still completes the lab, but their
   * mastery estimate is not inflated by a told answer (doc §7.5:
   * "assisted-flag propagation to BKT").
   *
   * Idempotent: calling it again once already flagged just re-returns the
   * same guidance without re-appending the flag.
   */
  async guidedFallback(
    attemptId: string,
    taskKey: string,
  ): Promise<GuidedFallback> {
    const task = await this.getTaskSpec(attemptId, taskKey);
    const hints = (task.hints ?? []).sort((a, b) => a.level - b.level);
    const usedMaxLevel = await this.getUsedMaxLevel(attemptId, taskKey);

    if (hints.length > 0 && hints.some((h) => h.level > usedMaxLevel)) {
      throw new BadRequestException(
        `hint ladder for task ${taskKey} is not yet exhausted -- reveal the remaining levels before the guided fallback (doc §7.5)`,
      );
    }

    const attempt = findOrThrow(
      await this.db
        .selectFrom('attempt.attempt')
        .select(['id', 'assistance_flags'])
        .where('id', '=', attemptId)
        .executeTakeFirst(),
      `attempt ${attemptId} not found`,
    );

    const text =
      hints.length > 0
        ? hints[hints.length - 1].text
        : `See the reference approach for task "${task.title ?? taskKey}".`;

    const alreadyFlagged =
      attempt.assistance_flags.includes(GUIDED_FALLBACK_FLAG);

    if (!alreadyFlagged) {
      await appendTypedEvent(this.events, {
        attemptId,
        actor: 'LEARNER',
        type: 'SOLUTION_VIEWED',
        payload: { task: taskKey, via: 'guided_fallback' },
      });

      await this.db.transaction().execute(async (trx) => {
        await trx
          .updateTable('attempt.attempt')
          .set({
            assistance_flags: sql`array_append(attempt.assistance_flags, ${GUIDED_FALLBACK_FLAG})`,
          })
          .where('id', '=', attemptId)
          .execute();

        // record the assist on the task row too, so replay / analytics
        // and the results screen can show which task was told.
        await trx
          .insertInto('attempt.attempt_task_state')
          .values({
            attempt_id: attemptId,
            task_key: taskKey,
            assisted: true,
          })
          .onConflict((oc) =>
            oc
              .columns(['attempt_id', 'task_key'])
              .doUpdateSet({ assisted: true }),
          )
          .execute();
      });
    }

    return { taskKey, text, assisted: true };
  }
}
