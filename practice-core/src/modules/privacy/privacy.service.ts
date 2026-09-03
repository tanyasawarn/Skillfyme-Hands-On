import { Inject, Injectable, Logger, NotFoundException } from '@nestjs/common';
import { Kysely } from 'kysely';
import { KYSELY } from '../../db/database.module';
import type { Database } from '../../db/schema';

/**
 * PLAN.md C16 / memory.md §883 -- learner-facing GDPR controls:
 *
 *   export():  everything the platform holds about this learner, as a
 *              single JSON archive (attempts + their event log + scores
 *              + artifacts + per-skill mastery).
 *
 *   erase():   anonymise the account and redact every PII-bearing
 *              child record, WHILE RETAINING AGGREGATE COUNTERS so
 *              analytics (§11.3) and the Elo/difficulty calibration
 *              stay correct. Specifically:
 *                - user_account: email -> a non-identifying tombstone,
 *                  status -> 'erased', erased_at stamped
 *                - attempt_events.payload -> {} for every event of the
 *                  learner's attempts (the append-only log keeps its
 *                  envelope -- seq/type/occurred_at -- for auditability,
 *                  but loses learner-authored content: typed commands,
 *                  incident-note text, hint context)
 *                - artifact.content -> null, storage_uri -> 'erased://'
 *                  (the object-store blob is tombstoned; a sweeper
 *                  deletes 'erased://' blobs out of band)
 *                - attempt.erased_at stamped
 *              NOT touched: the attempt rows themselves (kept, now
 *              pointing at an anonymised user_account), attempt_score
 *              (scoring math, no free text), learner_activity_state
 *              (best_score / status / counts).
 *
 * Idempotent: a second erase() for the same user is a no-op that still
 * returns the (already-zero) redaction counts.
 */
@Injectable()
export class PrivacyService {
  private readonly logger = new Logger(PrivacyService.name);

  constructor(@Inject(KYSELY) private readonly db: Kysely<Database>) {}

  async exportForUser(userId: string): Promise<LearnerDataExport> {
    const user = await this.db
      .selectFrom('learner.user_account')
      .select([
        'id',
        'tenant_id',
        'email',
        'role',
        'status',
        'created_at',
        'erased_at',
      ])
      .where('id', '=', userId)
      .executeTakeFirst();
    if (!user) throw new NotFoundException(`user ${userId} not found`);

    const attempts = await this.db
      .selectFrom('attempt.attempt')
      .selectAll()
      .where('user_id', '=', userId)
      .orderBy('created_at', 'asc')
      .execute();

    const attemptIds = attempts.map((a) => a.id);

    const [events, scores, artifacts, mastery, activityState] =
      await Promise.all([
        attemptIds.length
          ? this.db
              .selectFrom('attempt.attempt_events')
              .select([
                'attempt_id',
                'seq',
                'occurred_at',
                'actor',
                'type',
                'payload',
              ])
              .where('attempt_id', 'in', attemptIds)
              .orderBy('attempt_id', 'asc')
              .orderBy('seq', 'asc')
              .execute()
          : [],
        attemptIds.length
          ? this.db
              .selectFrom('attempt.attempt_score')
              .selectAll()
              .where('attempt_id', 'in', attemptIds)
              .execute()
          : [],
        attemptIds.length
          ? this.db
              .selectFrom('attempt.artifact')
              .select([
                'attempt_id',
                'kind',
                'storage_uri',
                'checksum',
                'content',
                'created_at',
              ])
              .where('attempt_id', 'in', attemptIds)
              .execute()
          : [],
        this.db
          .selectFrom('skill.skill_mastery')
          .selectAll()
          .where('user_id', '=', userId)
          .execute(),
        this.db
          .selectFrom('learner.learner_activity_state')
          .selectAll()
          .where('user_id', '=', userId)
          .execute(),
      ]);

    return {
      generated_at: new Date().toISOString(),
      user,
      attempts,
      attempt_events: events,
      attempt_scores: scores,
      artifacts,
      skill_mastery: mastery,
      learner_activity_state: activityState,
    };
  }

  async eraseForUser(userId: string): Promise<ErasureResult> {
    const user = await this.db
      .selectFrom('learner.user_account')
      .select(['id', 'erased_at'])
      .where('id', '=', userId)
      .executeTakeFirst();
    if (!user) throw new NotFoundException(`user ${userId} not found`);

    if (user.erased_at) {
      return {
        alreadyErased: true,
        erasedAt: new Date(user.erased_at).toISOString(),
        attemptsAnonymised: 0,
        eventPayloadsRedacted: 0,
        artifactsRedacted: 0,
      };
    }

    return this.db.transaction().execute(async (trx) => {
      const attempts = await trx
        .selectFrom('attempt.attempt')
        .select('id')
        .where('user_id', '=', userId)
        .execute();
      const attemptIds = attempts.map((a) => a.id);

      let eventPayloadsRedacted = 0;
      let artifactsRedacted = 0;

      if (attemptIds.length) {
        const evRes = await trx
          .updateTable('attempt.attempt_events')
          .set({ payload: JSON.stringify({}) })
          .where('attempt_id', 'in', attemptIds)
          .executeTakeFirst();
        eventPayloadsRedacted = Number(evRes.numUpdatedRows ?? 0);

        const artRes = await trx
          .updateTable('attempt.artifact')
          .set({ content: null, storage_uri: 'erased://' })
          .where('attempt_id', 'in', attemptIds)
          .executeTakeFirst();
        artifactsRedacted = Number(artRes.numUpdatedRows ?? 0);

        await trx
          .updateTable('attempt.attempt')
          .set({ erased_at: new Date() })
          .where('id', 'in', attemptIds)
          .execute();
      }

      // Anonymise the account last so a mid-transaction failure leaves it
      // clearly un-erased for a retry rather than half-done.
      const tombstone = `erased-${userId.slice(0, 8)}@deleted.invalid`;
      await trx
        .updateTable('learner.user_account')
        .set({ email: tombstone, status: 'erased', erased_at: new Date() })
        .where('id', '=', userId)
        .execute();

      this.logger.log(
        `erased user ${userId}: ${attemptIds.length} attempts anonymised, ` +
          `${eventPayloadsRedacted} event payloads redacted, ${artifactsRedacted} artifacts redacted`,
      );

      return {
        alreadyErased: false,
        erasedAt: new Date().toISOString(),
        attemptsAnonymised: attemptIds.length,
        eventPayloadsRedacted,
        artifactsRedacted,
      };
    });
  }
}

export interface LearnerDataExport {
  generated_at: string;
  user: unknown;
  attempts: unknown[];
  attempt_events: unknown[];
  attempt_scores: unknown[];
  artifacts: unknown[];
  skill_mastery: unknown[];
  learner_activity_state: unknown[];
}

export interface ErasureResult {
  alreadyErased: boolean;
  erasedAt: string;
  attemptsAnonymised: number;
  eventPayloadsRedacted: number;
  artifactsRedacted: number;
}
