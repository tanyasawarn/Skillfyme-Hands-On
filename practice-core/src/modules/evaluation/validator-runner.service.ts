import { Inject, Injectable, Logger } from '@nestjs/common';
import { Kysely } from 'kysely';
import { KYSELY } from '../../db/database.module';
import type { Database } from '../../db/schema';
import { EventStoreRepository } from '../event-store/event-store.repository';
import {
  VALIDATOR_EXECUTOR,
  type ValidatorExecutor,
  type ValidatorSpec,
} from './validator-executor.interface';

export interface TaskSpec {
  key: string;
  required: boolean;
  validators: ValidatorSpec[];
}

export interface RunValidationInput {
  attemptId: string;
  environmentId: string;
  scope: 'all' | { taskKey: string };
  trigger: 'manual' | 'auto_debounce' | 'submit';
  tasks: TaskSpec[];
}

export interface TaskValidationSummary {
  taskKey: string;
  required: boolean;
  passed: boolean; // all BLOCKING validators PASS (WARN-severity failures don't block)
  results: Array<{
    validatorId: string;
    status: string;
    severity: 'BLOCKING' | 'WARN';
    weight: number;
  }>;
}

/**
 * Doc §6.3 validation flow: enqueue -> mint creds -> resolve validator set
 * for scope -> execute in parallel with per-validator timeout -> emit
 * VALIDATOR_RESULT events (streamed in the real system; Phase 1 has no WS
 * layer yet so results are returned synchronously to the caller) ->
 * revoke creds -> task state recompute -> TASK_PASSED/TASK_FAILED events.
 *
 * "Resolve validator DAG for scope" (doc's phrase) is a full dependency
 * graph in the target architecture; Phase 1's validators don't declare
 * inter-validator dependencies yet (the activity spec schema has no such
 * field), so "resolve" here means "select the validators in scope" --
 * still real work (task-level and attempt-level scoping), just not a
 * literal graph walk until a real DAG is needed.
 */
@Injectable()
export class ValidatorRunnerService {
  private readonly logger = new Logger(ValidatorRunnerService.name);

  constructor(
    @Inject(KYSELY) private readonly db: Kysely<Database>,
    private readonly events: EventStoreRepository,
    @Inject(VALIDATOR_EXECUTOR) private readonly executor: ValidatorExecutor,
  ) {}

  async run(input: RunValidationInput): Promise<TaskValidationSummary[]> {
    const scope = input.scope;
    const tasksInScope =
      scope === 'all'
        ? input.tasks
        : input.tasks.filter((t) => t.key === scope.taskKey);

    const run = await this.db
      .insertInto('attempt.validation_run')
      .values({
        attempt_id: input.attemptId,
        scope: scope === 'all' ? 'all' : `task:${scope.taskKey}`,
        trigger: input.trigger,
        status: 'RUNNING',
      })
      .returningAll()
      .executeTakeFirstOrThrow();

    await this.events.append({
      attemptId: input.attemptId,
      actor: 'SYSTEM',
      type: 'VALIDATION_REQUESTED',
      payload: { scope: run.scope, trigger: input.trigger },
    });

    const summaries: TaskValidationSummary[] = [];

    for (const task of tasksInScope) {
      // Doc §6.3: "execute in parallel with per-validator timeout" --
      // parallel within a task; tasks themselves run sequentially here
      // for simplicity (Phase 1 activities are small, low validator
      // count -- revisit if a real activity's task count makes this slow).
      const results = await Promise.all(
        task.validators.map((v) =>
          this.executeWithTimeoutAndRetry(input.environmentId, v),
        ),
      );

      for (const result of results) {
        await this.db
          .insertInto('attempt.validator_result')
          .values({
            validation_run_id: run.id,
            validator_id: result.validatorId,
            status: result.status,
            observed: (result.observed ?? null) as never,
            expected: (result.expected ?? null) as never,
            duration_ms: result.durationMs,
            evidence_ref: result.evidenceRef ?? null,
          })
          .execute();

        // Doc §6.2: "ERROR is never scored against the learner... excluded
        // from scoring." VALIDATOR_RESULT event still records it for
        // on-call visibility, but it does not drive TASK_PASSED/FAILED.
        await this.events.append({
          attemptId: input.attemptId,
          actor: 'VALIDATOR',
          type: 'VALIDATOR_RESULT',
          payload: {
            validator_id: result.validatorId,
            status: result.status,
            observed: result.observed,
            expected: result.expected,
          },
        });
      }

      const scorable = results.filter((r) => r.status !== 'ERROR');
      const validatorMeta = new Map(
        task.validators.map((v) => [
          v.id,
          { severity: v.severity ?? ('BLOCKING' as const), weight: v.weight },
        ]),
      );
      const blockingResults = scorable.filter(
        (r) =>
          (validatorMeta.get(r.validatorId)?.severity ?? 'BLOCKING') ===
          'BLOCKING',
      );
      const taskPassed =
        blockingResults.length > 0 &&
        blockingResults.every((r) => r.status === 'PASS');

      await this.events.append({
        attemptId: input.attemptId,
        actor: 'SYSTEM',
        type: taskPassed ? 'TASK_PASSED' : 'TASK_FAILED',
        payload: { task: task.key },
      });

      summaries.push({
        taskKey: task.key,
        required: task.required,
        passed: taskPassed,
        results: results.map((r) => ({
          validatorId: r.validatorId,
          status: r.status,
          severity: validatorMeta.get(r.validatorId)?.severity ?? 'BLOCKING',
          weight: validatorMeta.get(r.validatorId)?.weight ?? 1,
        })),
      });
    }

    await this.db
      .updateTable('attempt.validation_run')
      .set({ status: 'COMPLETED', finished_at: new Date() })
      .where('id', '=', run.id)
      .execute();

    return summaries;
  }

  /**
   * Doc §6.2: "Every validator declares a timeout and retry policy...
   * K8S_ASSERT on readyReplicas should retry with backoff for up to 60s."
   * Phase 1 uses a fixed short backoff suitable for the fake executor;
   * real retry windows are per-validator-type policy the real executor
   * will own once it exists.
   */
  private async executeWithTimeoutAndRetry(
    environmentId: string,
    spec: ValidatorSpec,
  ) {
    const timeoutMs = spec.timeoutMs ?? 5000;
    const maxAttempts = 3;

    for (let attempt = 1; attempt <= maxAttempts; attempt++) {
      try {
        const result = await withTimeout(
          this.executor.execute(environmentId, spec),
          timeoutMs,
        );
        if (result.status !== 'ERROR' || attempt === maxAttempts) return result;
        this.logger.warn(
          `validator ${spec.id} ERRORed (attempt ${attempt}/${maxAttempts}), retrying`,
        );
      } catch {
        if (attempt === maxAttempts) {
          return {
            validatorId: spec.id,
            status: 'ERROR' as const,
            durationMs: timeoutMs,
            evidenceRef: 'timeout',
          };
        }
      }
      await sleep(100 * attempt);
    }
    // Unreachable, but keeps TypeScript satisfied about the loop's return type.
    return { validatorId: spec.id, status: 'ERROR' as const, durationMs: 0 };
  }
}

function withTimeout<T>(promise: Promise<T>, ms: number): Promise<T> {
  let timer: ReturnType<typeof setTimeout>;
  const timeout = new Promise<T>((_, reject) => {
    timer = setTimeout(() => reject(new Error('validator timeout')), ms);
  });
  // Clear the timer once either side settles -- otherwise a validator
  // that resolves well under its timeout still leaves a live timer
  // around for the rest of the window (observed as a Jest "did not exit"
  // open-handle warning after adding EvaluationService's evaluate() path).
  return Promise.race([promise, timeout]).finally(() => clearTimeout(timer));
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}
