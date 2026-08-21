import { Inject, Injectable } from '@nestjs/common';
import { sql, type Kysely } from 'kysely';
import { KYSELY } from '../../db/database.module';
import type { Database } from '../../db/schema';
import { BktService, type BktParams } from './bkt.service';

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

@Injectable()
export class MasteryService {
  constructor(
    @Inject(KYSELY) private readonly db: Kysely<Database>,
    private readonly bkt: BktService,
  ) {}

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

      await trx
        .insertInto('skill.skill_mastery')
        .values({
          user_id: input.userId,
          skill_id: input.skillId,
          p_mastery: result.pAfter,
          last_evidence_at: new Date(),
          evidence_count: 1,
          band,
        })
        .onConflict((oc) =>
          oc.columns(['user_id', 'skill_id']).doUpdateSet({
            p_mastery: result.pAfter,
            last_evidence_at: new Date(),
            evidence_count: sql`skill.skill_mastery.evidence_count + 1`,
            band,
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
      (id) => (masteryBySkill.get(id) ?? 0) >= 0.55,
    );
  }
}
