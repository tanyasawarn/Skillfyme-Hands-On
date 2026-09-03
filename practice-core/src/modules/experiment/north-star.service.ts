import { Inject, Injectable } from '@nestjs/common';
import { sql, Kysely } from 'kysely';
import { KYSELY } from '../../db/database.module';
import type { Database } from '../../db/schema';

/**
 * PLAN.md G11 / doc §11.4 -- north-star + guardrail metric instrumentation.
 *
 * NORTH STAR: **skill-mastery gain per learner-hour** = sum of POSITIVE
 * BKT deltas (skill.mastery_evidence.delta > 0) / active practice hours
 * (sum of attempt.active_seconds / 3600). Doc: "it resists the obvious
 * perverse incentives -- trivially easy labs raise completion but not
 * mastery gain; impossibly hard labs raise time but not gain."
 *
 * GUARDRAILS (doc §11.4): completion rate, abandonment, cost per attempt,
 * (learner satisfaction is a survey signal, not computable here).
 *
 * All metrics are computable per experiment variant by joining through
 * admin.experiment_assignment -- so a variant comparison is one call.
 */
@Injectable()
export class NorthStarService {
  constructor(@Inject(KYSELY) private readonly db: Kysely<Database>) {}

  /**
   * @param opts.experimentKey  when set, returns one row PER VARIANT of
   *                            that experiment; otherwise one overall row.
   * @param opts.sinceDays      window (default 30).
   */
  async metrics(
    opts: { experimentKey?: string; sinceDays?: number } = {},
  ): Promise<NorthStarRow[]> {
    const since = Math.max(1, Math.floor(opts.sinceDays ?? 30));

    // group key: 'all' or the variant name
    const groupExpr = opts.experimentKey
      ? sql<string>`COALESCE(ea.variant, 'unassigned')`
      : sql<string>`'all'`;

    // Pre-aggregate per-attempt so joining mastery_evidence (many rows
    // per attempt) never multiplies active_seconds / cost.
    const rows = await sql<{
      grp: string;
      positive_delta_sum: string;
      active_hours: string;
      attempts: string;
      completed: string;
      abandoned: string;
      total_cost_usd: string;
    }>`
      WITH scoped_attempts AS (
        SELECT a.id, a.user_id, a.status, a.active_seconds
          FROM attempt.attempt a
         WHERE a.created_at >= now() - make_interval(days => ${since})
      ),
      per_attempt AS (
        SELECT sa.id,
               sa.user_id,
               sa.status,
               sa.active_seconds,
               COALESCE((
                 SELECT SUM(CASE WHEN me.delta > 0 THEN me.delta ELSE 0 END)
                   FROM skill.mastery_evidence me
                  WHERE me.attempt_id = sa.id
               ), 0) AS pos_delta,
               COALESCE((
                 SELECT SUM(um.total_cost_usd)
                   FROM env.usage_meter um
                  WHERE um.attempt_id = sa.id::text
               ), 0) AS cost_usd
          FROM scoped_attempts sa
      )
      SELECT ${groupExpr} AS grp,
             COALESCE(SUM(pa.pos_delta), 0)                                  AS positive_delta_sum,
             COALESCE(SUM(pa.active_seconds), 0) / 3600.0                     AS active_hours,
             count(*)                                                        AS attempts,
             count(*) FILTER (WHERE pa.status IN ('PASSED','COMPLETED'))     AS completed,
             count(*) FILTER (WHERE pa.status IN ('ABANDONED','EXPIRED'))    AS abandoned,
             COALESCE(SUM(pa.cost_usd), 0)                                   AS total_cost_usd
        FROM per_attempt pa
        ${
          opts.experimentKey
            ? sql`LEFT JOIN admin.experiment_assignment ea
                    ON ea.user_id = pa.user_id
                   AND ea.experiment_key = ${opts.experimentKey}`
            : sql``
        }
       GROUP BY 1
       ORDER BY 1
    `.execute(this.db);

    return rows.rows.map((r) => {
      const gain = Number(r.positive_delta_sum);
      const hours = Number(r.active_hours);
      const attempts = Number(r.attempts);
      const completed = Number(r.completed);
      const abandoned = Number(r.abandoned);
      const cost = Number(r.total_cost_usd);
      return {
        group: r.grp,
        // NORTH STAR
        masteryGainPerLearnerHour: hours > 0 ? gain / hours : 0,
        // components
        positiveMasteryDeltaSum: gain,
        activeLearnerHours: hours,
        // GUARDRAILS
        attempts,
        completionRate: attempts > 0 ? completed / attempts : 0,
        abandonmentRate: attempts > 0 ? abandoned / attempts : 0,
        costPerAttemptUsd: attempts > 0 ? cost / attempts : 0,
      };
    });
  }
}

export interface NorthStarRow {
  group: string;
  masteryGainPerLearnerHour: number;
  positiveMasteryDeltaSum: number;
  activeLearnerHours: number;
  attempts: number;
  completionRate: number;
  abandonmentRate: number;
  costPerAttemptUsd: number;
}
