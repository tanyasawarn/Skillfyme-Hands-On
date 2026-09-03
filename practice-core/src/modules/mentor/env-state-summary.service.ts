import { Inject, Injectable } from '@nestjs/common';
import type { Kysely } from 'kysely';
import { KYSELY } from '../../db/database.module';
import type { Database } from '../../db/schema';

/**
 * PLAN.md G2 / doc §7.4 (D11) -- "Current environment state SUMMARY
 * (structured, refreshed on demand)". This is the read-only, structured
 * view of an attempt's runtime that the Mentor Service (G4) consumes
 * WITHOUT direct environment access:
 *
 *   - validator results: which tasks pass/fail, observed vs expected
 *   - recent commands + exit codes (last N, default 30)
 *   - recent stderr excerpts
 *   - k8s/cloud resource summary (best-effort; from FILE_CHANGED /
 *     resource-inventory events if present)
 *
 * It NEVER returns reference_solution or solution_apply content (those
 * aren't in the tables this reads), and it is attempt-scoped -- one
 * attempt's data only, no cross-learner leakage. The IAM boundary that
 * makes solution-unavailability structural (not just "we didn't include
 * it") is G3; this service simply has no code path to a solution.
 */
@Injectable()
export class EnvStateSummaryService {
  constructor(@Inject(KYSELY) private readonly db: Kysely<Database>) {}

  async summarize(
    attemptId: string,
    opts: { recentCommands?: number } = {},
  ): Promise<EnvStateSummary> {
    const recentN = Math.min(Math.max(opts.recentCommands ?? 30, 1), 100);

    const [taskStates, validatorResults, commandEvents, fileEvents] =
      await Promise.all([
        this.db
          .selectFrom('attempt.attempt_task_state')
          .select(['task_key', 'status', 'assisted', 'hints_used_max_level'])
          .where('attempt_id', '=', attemptId)
          .orderBy('task_key', 'asc')
          .execute(),

        // Latest validator result per validator_id for this attempt.
        this.db
          .selectFrom('attempt.validator_result as vr')
          .innerJoin(
            'attempt.validation_run as run',
            'run.id',
            'vr.validation_run_id',
          )
          .select([
            'vr.validator_id',
            'vr.status',
            'vr.observed',
            'vr.expected',
            'run.trigger',
            'run.started_at as run_at',
          ])
          .where('run.attempt_id', '=', attemptId)
          .orderBy('run.started_at', 'desc')
          .execute(),

        this.db
          .selectFrom('attempt.attempt_events')
          .select(['seq', 'occurred_at', 'payload'])
          .where('attempt_id', '=', attemptId)
          .where('type', '=', 'COMMAND_EXECUTED')
          .orderBy('seq', 'desc')
          .limit(recentN)
          .execute(),

        this.db
          .selectFrom('attempt.attempt_events')
          .select(['occurred_at', 'payload'])
          .where('attempt_id', '=', attemptId)
          .where('type', '=', 'FILE_CHANGED')
          .orderBy('seq', 'desc')
          .limit(50)
          .execute(),
      ]);

    // De-dupe validator results to the most recent per validator_id.
    const seenValidator = new Set<string>();
    const latestValidators: ValidatorResultSummary[] = [];
    for (const r of validatorResults) {
      if (seenValidator.has(r.validator_id)) continue;
      seenValidator.add(r.validator_id);
      latestValidators.push({
        validatorId: r.validator_id,
        status: r.status,
        observed: r.observed ?? null,
        expected: r.expected ?? null,
      });
    }

    const commands: CommandSummary[] = commandEvents
      .map((e) => {
        const p = (e.payload ?? {}) as {
          cmd?: string;
          exit_code?: number;
          duration_ms?: number;
        };
        return {
          cmd: typeof p.cmd === 'string' ? p.cmd : '',
          exitCode: typeof p.exit_code === 'number' ? p.exit_code : null,
          durationMs: typeof p.duration_ms === 'number' ? p.duration_ms : null,
          at: new Date(e.occurred_at).toISOString(),
        };
      })
      // events came newest-first for the LIMIT; present oldest-first.
      .reverse();

    // stderr excerpts: some COMMAND_EXECUTED payloads carry a bounded
    // stderr tail; surface the last few non-empty ones.
    const stderrExcerpts: string[] = [];
    for (const e of commandEvents) {
      const p = (e.payload ?? {}) as { stderr?: string };
      if (typeof p.stderr === 'string' && p.stderr.trim().length > 0) {
        stderrExcerpts.push(p.stderr.slice(0, 500));
        if (stderrExcerpts.length >= 5) break;
      }
    }

    const changedFiles = Array.from(
      new Set(
        fileEvents
          .map((e) => (e.payload as { path?: string })?.path)
          .filter((p): p is string => typeof p === 'string'),
      ),
    ).slice(0, 50);

    return {
      attemptId,
      generatedAt: new Date().toISOString(),
      tasks: taskStates.map((t) => ({
        taskKey: t.task_key,
        status: t.status,
        assisted: t.assisted,
        hintsUsedMaxLevel: t.hints_used_max_level,
      })),
      validators: latestValidators,
      recentCommands: commands,
      stderrExcerpts,
      resourceSummary: { changedFiles },
    };
  }
}

export interface EnvStateSummary {
  attemptId: string;
  generatedAt: string;
  tasks: Array<{
    taskKey: string;
    status: string;
    assisted: boolean;
    hintsUsedMaxLevel: number;
  }>;
  validators: ValidatorResultSummary[];
  recentCommands: CommandSummary[];
  stderrExcerpts: string[];
  resourceSummary: { changedFiles: string[] };
}

export interface ValidatorResultSummary {
  validatorId: string;
  status: 'PASS' | 'FAIL' | 'ERROR' | 'SKIP';
  observed: unknown;
  expected: unknown;
}

export interface CommandSummary {
  cmd: string;
  exitCode: number | null;
  durationMs: number | null;
  at: string;
}
