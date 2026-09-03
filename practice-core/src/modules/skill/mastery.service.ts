import { Inject, Injectable } from '@nestjs/common';
import { sql, type Kysely } from 'kysely';
import { KYSELY } from '../../db/database.module';
import type { Database } from '../../db/schema';
import { BktService, type BktParams } from './bkt.service';
import { MasteryConstants } from '../../common/constants';
import { EloService, type EloOutcome } from './elo.service';
import { computeReviewDueAt } from './spaced-repetition';

export interface RecordEvidenceInput {
  userId: string;
  skillId: string;
  attemptId: string;
  score: number;
  weight: number;
  passThreshold: number;
  difficultyAdjust: number;
  wasGenuineAttempt: boolean;
}

export interface RecordEloMatchInput {
  userId: string;
  /** Primary skill of the attempted activity -- doc §2.6 treats one attempt as one learner-vs-activity match, so exactly one skill anchors it. */
  skillId: string;
  activityVersionId: string;
  outcome: EloOutcome;
}

export interface RecordEloMatchResult {
  /** (activityElo - learnerElo)/400 computed from ratings *before* this match -- feeds BktService's difficultyAdjust, doc §2.4 step 2. */
  difficultyAdjust: number;
}

@Injectable()
export class MasteryService {
  constructor(
    @Inject(KYSELY) private readonly db: Kysely<Database>,
    private readonly bkt: BktService,
    private readonly elo: EloService,
  ) {}

  /**
   * Doc §2.6: "Treat each attempt as a match." Reads the learner's
   * current elo_rating for skillId (skill.skill_mastery.elo_rating,
   * defaulting to the population rating) and the activity's
   * difficulty_elo (content.activity_version, defaulting the same way),
   * runs one Elo match, and persists both sides in one transaction so
   * they can never drift out of sync with each other.
   *
   * Callers must call this *before* recordEvidence for the same
   * (user, skill) so recordEvidence's difficultyAdjust reflects the
   * pre-match ratings (doc §2.4 step 2 compares the activity's
   * difficulty against the learner's rating *at attempt time*, not
   * after this match has already moved it) -- and must call
   * recordEvidence for that same skill afterward regardless of this
   * method's result. If no skill_mastery row exists yet (a learner's
   * very first attempt at this skill), this method itself seeds one
   * (p_mastery from skill.bkt_p_init, elo_rating from this match's
   * result) rather than relying on recordEvidence to run first --
   * recordEvidence's own upsert then only touches p_mastery/evidence
   * fields on that same row, leaving elo_rating untouched.
   *
   * activityAttemptCount (for K_a's >500-attempt freeze) is a live count
   * of attempt.attempt rows against this activity_version rather than a
   * denormalised counter column -- attempt volume per activity is small
   * enough that this is not a hot-path concern, and it can never drift
   * from the attempt table itself.
   *
   * Both the learner's skill_mastery row and the activity_version row are
   * read with `FOR UPDATE` inside the transaction, holding a row lock for
   * its duration. Without this, two genuinely concurrent matches on the
   * same (user, skill) or activity (e.g. two attempts finishing at once)
   * can both read the same pre-match rating under Postgres's default READ
   * COMMITTED isolation and unknowingly overwrite each other's update --
   * a lost-update race. The lock serialises concurrent matches instead of
   * losing one silently.
   */
  async recordEloMatch(
    input: RecordEloMatchInput,
  ): Promise<RecordEloMatchResult> {
    return this.db.transaction().execute(async (trx) => {
      const learnerRow = await trx
        .selectFrom('skill.skill_mastery')
        .select('elo_rating')
        .where('user_id', '=', input.userId)
        .where('skill_id', '=', input.skillId)
        .forUpdate()
        .executeTakeFirst();
      const learnerElo =
        learnerRow?.elo_rating != null
          ? Number(learnerRow.elo_rating)
          : this.elo.defaultRating();

      const activityRow = await trx
        .selectFrom('content.activity_version')
        .select('difficulty_elo')
        .where('id', '=', input.activityVersionId)
        .forUpdate()
        .executeTakeFirstOrThrow();
      const activityElo =
        activityRow.difficulty_elo != null
          ? Number(activityRow.difficulty_elo)
          : this.elo.defaultRating();

      const learnerMatches = await trx
        .selectFrom('skill.mastery_evidence')
        .select((eb) => eb.fn.countAll().as('count'))
        .where('user_id', '=', input.userId)
        .where('skill_id', '=', input.skillId)
        .executeTakeFirst();
      const learnerMatchCount = Number(learnerMatches?.count ?? 0);

      const activityAttempts = await trx
        .selectFrom('attempt.attempt')
        .select((eb) => eb.fn.countAll().as('count'))
        .where('activity_version_id', '=', input.activityVersionId)
        .executeTakeFirst();
      const activityAttemptCount = Number(activityAttempts?.count ?? 0);

      const result = this.elo.update({
        learnerElo,
        activityElo,
        learnerMatchCount,
        activityAttemptCount,
        outcome: input.outcome,
      });

      // Upsert rather than update-only: a learner's very first Elo match
      // in a skill-cluster (no prior skill_mastery row) must still
      // persist a rating, not silently no-op. p_mastery has no default
      // and NOT NULL -- seed it from the skill's own bkt_p_init so a
      // fresh row is never invented with an arbitrary mastery value. If
      // recordEvidence has already run for this (user, skill) this
      // insert conflicts harmlessly and only elo_rating is touched.
      if (!learnerRow) {
        const skillDefaults = await trx
          .selectFrom('skill.skill')
          .select('bkt_p_init')
          .where('id', '=', input.skillId)
          .executeTakeFirstOrThrow();
        await trx
          .insertInto('skill.skill_mastery')
          .values({
            user_id: input.userId,
            skill_id: input.skillId,
            p_mastery: skillDefaults.bkt_p_init,
            elo_rating: result.learnerElo,
          })
          .onConflict((oc) =>
            oc.columns(['user_id', 'skill_id']).doUpdateSet({
              elo_rating: result.learnerElo,
            }),
          )
          .execute();
      } else {
        await trx
          .updateTable('skill.skill_mastery')
          .set({ elo_rating: result.learnerElo })
          .where('user_id', '=', input.userId)
          .where('skill_id', '=', input.skillId)
          .execute();
      }

      // Doc §2.6 "recalibrated nightly from attempt outcomes" -- run
      // synchronously per-match here instead, which is strictly more
      // current than a nightly batch and needs no separate job.
      // migration 0007 narrows the PUBLISHED-immutability trigger to
      // permit exactly this column.
      await trx
        .updateTable('content.activity_version')
        .set({ difficulty_elo: result.activityElo })
        .where('id', '=', input.activityVersionId)
        .execute();

      return { difficultyAdjust: result.difficultyAdjust };
    });
  }

  /**
   * Doc §2.4 step 5 + §8.4 "mastery_evidence links every mastery change to
   * the attempt that caused it — full explainability." Reads current
   * mastery (or falls back to skill.bkt_p_init), runs the BKT update, then
   * writes both skill_mastery (upsert) and mastery_evidence (append) in one
   * transaction so the two never drift apart.
   */
  async recordEvidence(
    input: RecordEvidenceInput,
  ): Promise<{ pBefore: number; pAfter: number }> {
    return this.db.transaction().execute(async (trx) => {
      const skill = await trx
        .selectFrom('skill.skill')
        .select([
          'bkt_p_init',
          'bkt_p_transit',
          'bkt_p_slip',
          'bkt_p_guess',
          'decay_half_life_days',
        ])
        .where('id', '=', input.skillId)
        .executeTakeFirstOrThrow();

      const existing = await trx
        .selectFrom('skill.skill_mastery')
        .selectAll()
        .where('user_id', '=', input.userId)
        .where('skill_id', '=', input.skillId)
        .executeTakeFirst();

      const params: BktParams = {
        pInit: Number(skill.bkt_p_init),
        pTransit: Number(skill.bkt_p_transit),
        pSlip: Number(skill.bkt_p_slip),
        pGuess: Number(skill.bkt_p_guess),
      };

      const priorP = existing
        ? this.bkt.decayedMastery(
            Number(existing.p_mastery),
            params.pInit,
            existing.last_evidence_at
              ? new Date(existing.last_evidence_at)
              : new Date(),
            skill.decay_half_life_days,
          )
        : params.pInit;

      const result = this.bkt.update({
        priorP,
        params,
        score: input.score,
        weight: input.weight,
        passThreshold: input.passThreshold,
        difficultyAdjust: input.difficultyAdjust,
        wasGenuineAttempt: input.wasGenuineAttempt,
      });

      const band = this.bkt.bandFor(result.pAfter);
      const now = new Date();
      // PLAN.md G9 / doc §2.4 "review-due": once a skill reaches Competent+
      // it enters the spaced-repetition rotation. review_due_at is set
      // forward by a band-scaled interval (and lengthened on an on-time
      // review); cleared to null while the skill is still below Competent.
      const reviewDueAt = computeReviewDueAt(
        band,
        now,
        existing?.review_due_at ?? null,
      );

      await trx
        .insertInto('skill.skill_mastery')
        .values({
          user_id: input.userId,
          skill_id: input.skillId,
          p_mastery: result.pAfter,
          last_evidence_at: now,
          evidence_count: 1,
          band,
          review_due_at: reviewDueAt,
        })
        .onConflict((oc) =>
          oc.columns(['user_id', 'skill_id']).doUpdateSet({
            p_mastery: result.pAfter,
            last_evidence_at: now,
            evidence_count: sql`skill.skill_mastery.evidence_count + 1`,
            band,
            review_due_at: reviewDueAt,
          }),
        )
        .execute();

      await trx
        .insertInto('skill.mastery_evidence')
        .values({
          user_id: input.userId,
          skill_id: input.skillId,
          attempt_id: input.attemptId,
          delta: result.delta,
          p_before: result.pBefore,
          p_after: result.pAfter,
          weight: input.weight,
        })
        .execute();

      return { pBefore: result.pBefore, pAfter: result.pAfter };
    });
  }

  async getMastery(userId: string, skillId: string) {
    return this.db
      .selectFrom('skill.skill_mastery')
      .selectAll()
      .where('user_id', '=', userId)
      .where('skill_id', '=', skillId)
      .executeTakeFirst();
  }

  /** Doc §1.2 Skills nav: "skill graph view, mastery per skill." */
  /**
   * courseSlug scopes this to one course's skills -- without it, a
   * learner with mastery evidence in both DevOps-with-AI and
   * GenAI-with-ML would see both curricula's skills mixed into one flat
   * list with no indication which course each belongs to. Matches
   * CatalogRepository.listSkillDrivenCatalog's course scoping.
   */
  async listMasteryForUser(userId: string, courseSlug: string) {
    return this.db
      .selectFrom('skill.skill_mastery as sm')
      .innerJoin('skill.skill as s', 's.id', 'sm.skill_id')
      .select([
        's.id as skill_id',
        's.slug',
        's.name',
        's.domain',
        'sm.p_mastery',
        'sm.band',
        'sm.evidence_count',
        'sm.last_evidence_at',
      ])
      .where('sm.user_id', '=', userId)
      .where('s.course_slug', '=', courseSlug)
      .orderBy('sm.p_mastery', 'desc')
      .execute();
  }

  /** Doc §2.5 stage 2 eligibility gate: REQUIRES-prereq mastery < 0.55 blocks the activity. */
  async meetsRequiresGate(
    userId: string,
    requiresAncestorSkillIds: string[],
  ): Promise<boolean> {
    if (requiresAncestorSkillIds.length === 0) return true;
    const rows = await this.db
      .selectFrom('skill.skill_mastery')
      .select(['skill_id', 'p_mastery'])
      .where('user_id', '=', userId)
      .where('skill_id', 'in', requiresAncestorSkillIds)
      .execute();

    const masteryBySkill = new Map(
      rows.map((r) => [r.skill_id, Number(r.p_mastery)]),
    );
    return requiresAncestorSkillIds.every(
      (id) =>
        (masteryBySkill.get(id) ?? 0) >=
        MasteryConstants.REQUIRES_GATE_THRESHOLD,
    );
  }
}
