import { Inject, Injectable } from '@nestjs/common';
import type { Kysely } from 'kysely';
import { KYSELY } from '../db/database.module';
import type { Database } from '../db/schema';
import type { ActivitySpec } from '../modules/catalog/activity-spec';

/**
 * PLAN.md Phase 4's S5: the attempt->activity_version->spec_jsonb join
 * was independently rewritten in 4 real call sites (hint.service.ts,
 * artifact.service.ts x2, command-executed.consumer.ts) -- not the same
 * need as attempt.service.ts's/evaluation.service.ts's own
 * `selectAll()` version lookups, which read other columns (blueprint_id,
 * mode, etc) too and are deliberately left as their own queries rather
 * than forced through this narrower method.
 *
 * Lives in `common/`, not on `AttemptRepository`, specifically because
 * `AttemptModule` already imports `EvaluationModule` (for
 * `EvaluationService`) -- putting this on `AttemptRepository` and
 * injecting it into `ArtifactService` (evaluation module) would require
 * `EvaluationModule` to import `AttemptModule` back, a circular module
 * dependency. A small standalone provider both modules can import
 * without circularity, same reasoning `BaseGrpcClient`/
 * `NatsSubscriberBase` already live in `common/` rather than any one
 * feature module.
 *
 * Returns `undefined` on a missing attempt rather than throwing --
 * command-executed.consumer.ts (a NATS consumer processing a
 * possibly-stale/deleted attempt) needs to return `null` and move on,
 * not throw; callers that DO need a hard failure wrap this in
 * `findOrThrow` (U5) at their own call site, same as any other
 * possibly-missing repository read.
 */
@Injectable()
export class ActivitySpecReader {
  constructor(@Inject(KYSELY) private readonly db: Kysely<Database>) {}

  async getActivitySpec(attemptId: string): Promise<ActivitySpec | undefined> {
    const row = await this.db
      .selectFrom('attempt.attempt as a')
      .innerJoin(
        'content.activity_version as v',
        'v.id',
        'a.activity_version_id',
      )
      .select('v.spec_jsonb')
      .where('a.id', '=', attemptId)
      .executeTakeFirst();
    return row?.spec_jsonb as ActivitySpec | undefined;
  }
}
