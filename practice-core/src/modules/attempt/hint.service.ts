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
}
