import { Inject, Injectable, Logger } from '@nestjs/common';
import { createHash } from 'node:crypto';
import { Kysely } from 'kysely';
import { KYSELY } from '../../db/database.module';
import type { Database } from '../../db/schema';

/**
 * PLAN.md G11 / doc §11.4 -- A/B assignment.
 *
 * "Version and A/B everything that is a judgement call: recommendation
 * ranker weights, hint ladder wording, difficulty defaults, idle
 * timeouts, mentor personas. Assignment at the learner level, sticky,
 * logged on every recommendation and attempt."
 *
 * assign(): returns the learner's variant for an experiment. First
 * exposure buckets them via a deterministic hash of
 * (experiment_key | user_id) mapped over the variant weight ranges,
 * then PERSISTS the choice so a later change to the split never
 * re-buckets an already-enrolled learner. Subsequent calls read the
 * persisted row.
 *
 * If the experiment is unknown or CONCLUDED, assign() returns 'control'
 * without writing -- callers get the default behavior.
 */
@Injectable()
export class ExperimentService {
  private readonly logger = new Logger(ExperimentService.name);

  constructor(@Inject(KYSELY) private readonly db: Kysely<Database>) {}

  async assign(experimentKey: string, userId: string): Promise<string> {
    const existing = await this.db
      .selectFrom('admin.experiment_assignment')
      .select('variant')
      .where('experiment_key', '=', experimentKey)
      .where('user_id', '=', userId)
      .executeTakeFirst();
    if (existing) return existing.variant;

    const exp = await this.db
      .selectFrom('admin.experiment')
      .select(['variants_jsonb', 'status'])
      .where('key', '=', experimentKey)
      .executeTakeFirst();
    if (!exp || exp.status !== 'RUNNING') return 'control';

    const variants = exp.variants_jsonb as Array<{
      name: string;
      weight: number;
    }>;
    const variant = this.bucket(experimentKey, userId, variants);

    // Persist. ON CONFLICT DO NOTHING covers the race where two requests
    // for the same learner arrive together -- the loser re-reads.
    await this.db
      .insertInto('admin.experiment_assignment')
      .values({ experiment_key: experimentKey, user_id: userId, variant })
      .onConflict((oc) => oc.columns(['experiment_key', 'user_id']).doNothing())
      .execute();

    const row = await this.db
      .selectFrom('admin.experiment_assignment')
      .select('variant')
      .where('experiment_key', '=', experimentKey)
      .where('user_id', '=', userId)
      .executeTakeFirst();
    return row?.variant ?? variant;
  }

  /** Deterministic hash -> [0,1) -> variant by cumulative weight. */
  private bucket(
    key: string,
    userId: string,
    variants: Array<{ name: string; weight: number }>,
  ): string {
    if (variants.length === 0) return 'control';
    const total = variants.reduce((s, v) => s + Math.max(0, v.weight), 0);
    if (total <= 0) return variants[0].name;

    const h = createHash('sha256').update(`${key}|${userId}`).digest();
    // first 4 bytes -> uint32 -> [0,1)
    const n = h.readUInt32BE(0) / 0x100000000;
    let cursor = 0;
    for (const v of variants) {
      cursor += Math.max(0, v.weight) / total;
      if (n < cursor) return v.name;
    }
    return variants[variants.length - 1].name;
  }

  /** For dashboards: current enrolment per variant. */
  async enrolment(
    experimentKey: string,
  ): Promise<Array<{ variant: string; n: number }>> {
    const rows = await this.db
      .selectFrom('admin.experiment_assignment')
      .select((eb) => ['variant', eb.fn.countAll<string>().as('n')])
      .where('experiment_key', '=', experimentKey)
      .groupBy('variant')
      .execute();
    return rows.map((r) => ({ variant: r.variant, n: Number(r.n) }));
  }
}
