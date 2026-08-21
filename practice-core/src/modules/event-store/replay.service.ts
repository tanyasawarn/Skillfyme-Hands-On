import { Inject, Injectable, Logger } from '@nestjs/common';
import type { Kysely } from 'kysely';
import { KYSELY } from '../../db/database.module';
import type { Database } from '../../db/schema';
import { EventStoreRepository } from './event-store.repository';

interface TaskPassedPayload {
  task: string;
}
interface TaskFailedPayload {
  task: string;
}
interface TaskSkippedPayload {
  task: string;
}
interface HintRequestedPayload {
  task: string;
  level: number;
}

/**
 * Doc §4.1 layer 2 + §4.4: "attempt_task_state (materialised per-task
 * status, updated by validator results) -- this is what the UI reads...
 * Rebuilding layer 2 from layer 1 must always be possible; write a replay
 * tool in week one and test it in CI. It is your recovery mechanism for
 * every data bug you will have." (also doc §13.5 top-5 item #1 territory,
 * adjacent to the attempt_id correlation requirement).
 *
 * This service is intentionally pure event-folding: given the ordered
 * event log for one attempt, it derives attempt_task_state from scratch.
 * rebuildForAttempt() then persists that derived state, so a corrupted or
 * lost attempt_task_state row is always recoverable as long as
 * attempt_events (the source of truth, D3) survives.
 */
@Injectable()
export class ReplayService {
  private readonly logger = new Logger(ReplayService.name);

  constructor(
    @Inject(KYSELY) private readonly db: Kysely<Database>,
    private readonly eventStore: EventStoreRepository,
  ) {}

  /** Pure fold: event log -> derived per-task state. No I/O beyond the read. */
  deriveTaskState(
    events: Array<{ type: string; payload: unknown; occurred_at: Date }>,
  ): Map<
    string,
    {
      status: 'PENDING' | 'PASSED' | 'FAILED' | 'SKIPPED';
      firstPassAt: Date | null;
      attemptsCount: number;
      hintsUsedMaxLevel: number;
      skipped: boolean;
      assisted: boolean;
    }
  > {
    const state = new Map<
      string,
      {
        status: 'PENDING' | 'PASSED' | 'FAILED' | 'SKIPPED';
        firstPassAt: Date | null;
        attemptsCount: number;
        hintsUsedMaxLevel: number;
        skipped: boolean;
        assisted: boolean;
      }
    >();

    const ensure = (task: string) => {
      if (!state.has(task)) {
        state.set(task, {
          status: 'PENDING',
          firstPassAt: null,
          attemptsCount: 0,
          hintsUsedMaxLevel: 0,
          skipped: false,
          assisted: false,
        });
      }
      return state.get(task)!;
    };

    for (const event of events) {
      switch (event.type) {
        case 'TASK_PASSED': {
          const { task } = event.payload as TaskPassedPayload;
          const s = ensure(task);
          s.attemptsCount += 1;
          if (s.status !== 'PASSED') {
            s.status = 'PASSED';
            s.firstPassAt = event.occurred_at;
          }
          break;
        }
        case 'TASK_FAILED': {
          const { task } = event.payload as TaskFailedPayload;
          const s = ensure(task);
          s.attemptsCount += 1;
          // A later PASSED for the same task overrides this; a FAILED
          // never demotes an already-PASSED task (doc: pass state is
          // sticky once achieved within an attempt).
          if (s.status !== 'PASSED') s.status = 'FAILED';
          break;
        }
        case 'TASK_SKIPPED': {
          const { task } = event.payload as TaskSkippedPayload;
          const s = ensure(task);
          s.status = 'SKIPPED';
          s.skipped = true;
          s.assisted = true; // doc §7.5: skip applies a repair action, contributes no positive BKT evidence
          break;
        }
        case 'HINT_REQUESTED': {
          const { task, level } = event.payload as HintRequestedPayload;
          const s = ensure(task);
          s.hintsUsedMaxLevel = Math.max(s.hintsUsedMaxLevel, level);
          break;
        }
        default:
          break; // lifecycle/execution/scenario events don't affect per-task state
      }
    }

    return state;
  }

  /**
   * Rebuilds and persists attempt_task_state for one attempt from its
   * event log. Idempotent: safe to run repeatedly (e.g. as a scheduled
   * consistency check, or manually after a suspected data bug).
   */
  async rebuildForAttempt(attemptId: string): Promise<number> {
    const events = await this.eventStore.replay(attemptId);
    const derived = this.deriveTaskState(events);

    await this.db.transaction().execute(async (trx) => {
      await trx
        .deleteFrom('attempt.attempt_task_state')
        .where('attempt_id', '=', attemptId)
        .execute();

      for (const [taskKey, s] of derived) {
        await trx
          .insertInto('attempt.attempt_task_state')
          .values({
            attempt_id: attemptId,
            task_key: taskKey,
            status: s.status,
            first_pass_at: s.firstPassAt,
            attempts_count: s.attemptsCount,
            hints_used_max_level: s.hintsUsedMaxLevel,
            skipped: s.skipped,
            assisted: s.assisted,
          })
          .execute();
      }
    });

    this.logger.log(
      `Rebuilt attempt_task_state for attempt ${attemptId}: ${derived.size} tasks`,
    );
    return derived.size;
  }
}
