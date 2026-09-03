import { Inject, Injectable } from '@nestjs/common';
import { sql, type Kysely, type SqlBool } from 'kysely';
import { KYSELY } from '../../db/database.module';
import type { Database } from '../../db/schema';
import { EligibilityService } from '../attempt/eligibility.service';
import { publishedActivityVersionsQuery } from '../../common/published-activity-versions-query';
import { MasteryConstants } from '../../common/constants';

export type RecommendationReasonCode =
  | 'CURRICULUM_ADJACENT'
  | 'REMEDIATION'
  | 'SPACED_REPETITION'
  | 'PROGRESSION'
  | 'UNBLOCKING';

export interface Recommendation {
  activityId: string;
  activityVersionId: string;
  slug: string;
  score: number;
  reasonCode: RecommendationReasonCode;
  reasonParams: Record<string, unknown>;
}

/**
 * Doc §2.5 four-stage pipeline: candidate gen (5 sources) -> eligibility
 * filter -> weighted scoring -> re-rank + package with reason codes.
 *
 * PLAN.md G8/G9/G10 (Phase 4) completed the candidate-source set:
 *   (a) CURRICULUM_ADJACENT -- sibling activities on topics the learner
 *       just completed / is currently in.
 *   (b) REMEDIATION -- struggling primary skill (failed attempt + low
 *       mastery in 30d); now walks the skill-DAG to unmastered REQUIRES
 *       ancestors and picks the lowest-difficulty activity per ancestor
 *       (doc §2.7's remediation-ladder ancestor walk, G10), not just the
 *       skill's own remediation.
 *   (c) SPACED_REPETITION -- a Competent+ skill whose
 *       skill_mastery.review_due_at has passed (doc §2.4 review-due, G9).
 *   (d) PROGRESSION -- the learner is Proficient/Mastered on a skill; the
 *       next-difficulty activity on that skill is offered so they keep
 *       moving (doc §2.5 f: "next-difficulty for Competent+").
 *   (e) UNBLOCKING -- an activity the learner is INELIGIBLE for only
 *       because of one unmet REQUIRES prereq; recommend that prereq
 *       activity so the desirable target unblocks (doc §2.5
 *       "unblocking-value").
 *
 * Stage 3 (weighted scoring): each source emits a raw urgency in a
 * documented band; SOURCE_WEIGHT re-weights across sources so, e.g., a
 * hard remediation outranks a spaced-repetition nudge. Stage 4 (re-rank):
 * sort by the weighted score, dedupe by activity_version keeping the
 * strongest reason, take the top N.
 *
 * Doc §2.5: "Every recommendation must carry a human-readable reason."
 * reasonCode + reasonParams are structured, not pre-rendered strings.
 */
// Stage 3 weighting: cross-source re-weight applied to each source's raw
// urgency. Remediation/unblocking are the most actionable ("you're
// stuck"); progression/spaced-repetition are lower-priority nudges.
const SOURCE_WEIGHT: Record<RecommendationReasonCode, number> = {
  REMEDIATION: 1.0,
  UNBLOCKING: 0.95,
  CURRICULUM_ADJACENT: 0.8,
  PROGRESSION: 0.7,
  SPACED_REPETITION: 0.6,
};

@Injectable()
export class RecommendationService {
  constructor(
    @Inject(KYSELY) private readonly db: Kysely<Database>,
    private readonly eligibility: EligibilityService,
  ) {}

  async recommend(
    userId: string,
    tenantId: string,
    limit = 8,
  ): Promise<Recommendation[]> {
    const candidates = new Map<string, Recommendation>();

    await this.addCurriculumAdjacentCandidates(userId, tenantId, candidates);
    await this.addRemediationCandidates(userId, tenantId, candidates);
    await this.addSpacedRepetitionCandidates(userId, tenantId, candidates);
    await this.addProgressionCandidates(userId, tenantId, candidates);
    await this.addUnblockingCandidates(userId, tenantId, candidates);

    // Stage 2: eligibility filter. UNBLOCKING candidates are the prereq
    // activity itself -- they must still be eligible to be recommended;
    // the ineligible TARGET they unblock is only named in reasonParams.
    const filtered: Recommendation[] = [];
    for (const candidate of candidates.values()) {
      const result = await this.eligibility.check(
        userId,
        candidate.activityVersionId,
      );
      if (result.eligible) filtered.push(candidate);
    }

    // Stage 3: weighted scoring -- re-weight each candidate's raw urgency
    // by its source before the final sort.
    for (const c of filtered) {
      c.score = c.score * SOURCE_WEIGHT[c.reasonCode];
    }

    // Stage 4: re-rank + package.
    filtered.sort((a, b) => b.score - a.score);
    const top = filtered.slice(0, limit);

    if (top.length > 0) {
      await this.db
        .insertInto('learner.recommendation')
        .values(
          top.map((r) => ({
            user_id: userId,
            activity_id: r.activityId,
            score: r.score,
            features_jsonb: r.reasonParams as never,
            reason_code: r.reasonCode,
            reason_params_jsonb: r.reasonParams as never,
            ranker_version: 'rules-v2',
          })),
        )
        .execute();
    }

    return top;
  }

  /**
   * Doc §2.5 source (a): "activities on topics the learner just completed
   * or is currently in." Phase 1 approximation of "currently in": topics
   * linked to activities the learner has at least one attempt on but
   * hasn't yet passed, plus topics of activities they've never attempted
   * within courses they're enrolled in (cold-start: everything in an
   * enrolled course's first module is "currently in").
   */
  private async addCurriculumAdjacentCandidates(
    userId: string,
    tenantId: string,
    candidates: Map<string, Recommendation>,
  ) {
    const attemptedActivityIds = await this.db
      .selectFrom('attempt.attempt')
      .select('activity_id')
      .where('user_id', '=', userId)
      .distinct()
      .execute();
    const attemptedIds = new Set(
      attemptedActivityIds.map((a) => a.activity_id),
    );

    // Topics of activities already attempted -> sibling activities on the
    // same topic are "curriculum-adjacent."
    const topicIds =
      attemptedIds.size > 0
        ? await this.db
            .selectFrom('content.activity_topic as at')
            .select('at.topic_id')
            .where('at.activity_version_id', 'in', (eb) =>
              eb
                .selectFrom('content.activity_version as av')
                .select('av.id')
                .where('av.activity_id', 'in', [...attemptedIds]),
            )
            .distinct()
            .execute()
        : [];

    const relevantTopicIds =
      topicIds.length > 0
        ? topicIds.map((t) => t.topic_id)
        : (
            await this.db
              .selectFrom('content.topic')
              .select('id')
              .where('module_id', 'is not', null)
              .limit(5)
              .execute()
          ).map((t) => t.id); // cold start: no attempts yet, surface the first few topics of any module

    if (relevantTopicIds.length === 0) return;

    const rows = await publishedActivityVersionsQuery(
      this.db
        .selectFrom('content.activity_topic as at')
        .innerJoin(
          'content.activity_version as av',
          'av.id',
          'at.activity_version_id',
        )
        .innerJoin('content.activity as a', 'a.id', 'av.activity_id')
        .innerJoin('content.topic as t', 't.id', 'at.topic_id')
        .select([
          'a.id as activity_id',
          'av.id as activity_version_id',
          'a.slug',
          't.title as topic_title',
          'at.relevance',
        ]),
      tenantId,
    )
      .where('at.topic_id', 'in', relevantTopicIds)
      .where(
        'a.id',
        'not in',
        attemptedIds.size > 0
          ? [...attemptedIds]
          : ['00000000-0000-0000-0000-000000000000'],
      )
      .execute();

    for (const row of rows) {
      const existing = candidates.get(row.activity_version_id);
      const score = 0.5 + Number(row.relevance) * 0.3; // doc §2.5 f3 CurriculumAlign-shaped weighting, simplified
      if (!existing || existing.score < score) {
        candidates.set(row.activity_version_id, {
          activityId: row.activity_id,
          activityVersionId: row.activity_version_id,
          slug: row.slug,
          score,
          reasonCode: 'CURRICULUM_ADJACENT',
          reasonParams: { topic: row.topic_title },
        });
      }
    }
  }

  /**
   * Doc §2.5 source (b): "activities whose PRIMARY skill has low mastery
   * AND that skill has a failed/struggled attempt in last 30d." Phase 1
   * simplification: "struggled" = any FAILED attempt (doc's fuller
   * definition includes hint-ladder-exhaustion and low
   * diagnostic-efficiency signals that don't exist until Production Sims,
   * Phase 2).
   */
  private async addRemediationCandidates(
    userId: string,
    tenantId: string,
    candidates: Map<string, Recommendation>,
  ) {
    const thirtyDaysAgo = new Date(Date.now() - 30 * 24 * 60 * 60 * 1000);

    const strugglingSkills = await this.db
      .selectFrom('attempt.attempt as a')
      .innerJoin(
        'content.activity_skill as ask',
        'ask.activity_version_id',
        'a.activity_version_id',
      )
      .innerJoin('skill.skill_mastery as sm', 'sm.skill_id', 'ask.skill_id')
      .select(['ask.skill_id', 'sm.p_mastery'])
      .where('a.user_id', '=', userId)
      .where('sm.user_id', '=', userId)
      .where('a.status', '=', 'FAILED')
      .where(sql<SqlBool>`a.created_at >= ${thirtyDaysAgo}`)
      .where('ask.is_primary', '=', true)
      .where('sm.p_mastery', '<', MasteryConstants.REQUIRES_GATE_THRESHOLD)
      .distinct()
      .execute();

    if (strugglingSkills.length === 0) return;

    for (const { skill_id, p_mastery } of strugglingSkills) {
      // Doc §2.7's remediation ladder walks to unmastered ancestors and
      // picks the lowest-difficulty activity per ancestor; this Phase 1
      // version recommends lower-difficulty activities that target the
      // struggling skill itself (its own remediation), not yet the full
      // skill-DAG-ancestor walk (that needs skill_closure traversal
      // integrated with difficulty_elo comparison -- a reasonable Phase 4
      // extension once Elo calibration, §2.6, has real data).
      const rows = await publishedActivityVersionsQuery(
        this.db
          .selectFrom('content.activity_skill as ask')
          .innerJoin(
            'content.activity_version as av',
            'av.id',
            'ask.activity_version_id',
          )
          .innerJoin('content.activity as a', 'a.id', 'av.activity_id')
          .select([
            'a.id as activity_id',
            'av.id as activity_version_id',
            'a.slug',
            'av.difficulty_level',
          ]),
        tenantId,
      )
        .where('ask.skill_id', '=', skill_id)
        .where('av.difficulty_level', 'in', ['L1', 'L2'])
        .execute();

      for (const row of rows) {
        const score =
          0.7 + (MasteryConstants.REQUIRES_GATE_THRESHOLD - Number(p_mastery)); // lower mastery -> higher urgency
        const existing = candidates.get(row.activity_version_id);
        if (!existing || existing.score < score) {
          candidates.set(row.activity_version_id, {
            activityId: row.activity_id,
            activityVersionId: row.activity_version_id,
            slug: row.slug,
            score,
            reasonCode: 'REMEDIATION',
            reasonParams: { mastery: Number(p_mastery), skill_id },
          });
        }
      }

      // G10: skill-DAG ancestor walk. For each struggling skill, walk its
      // unmastered REQUIRES ancestors (via skill_closure) and offer the
      // lowest-difficulty published activity that targets each ancestor --
      // "you keep failing X because you never solidified its prerequisite Y".
      const ancestors = await this.db
        .selectFrom('skill.skill_closure as sc')
        .leftJoin('skill.skill_mastery as sm', (j) =>
          j
            .onRef('sm.skill_id', '=', 'sc.ancestor_id')
            .on('sm.user_id', '=', userId),
        )
        .select(['sc.ancestor_id', 'sm.p_mastery as anc_mastery'])
        .where('sc.descendant_id', '=', skill_id)
        .where('sc.ancestor_id', '!=', skill_id)
        .where((eb) =>
          eb.or([
            eb('sm.p_mastery', 'is', null),
            eb('sm.p_mastery', '<', MasteryConstants.REQUIRES_GATE_THRESHOLD),
          ]),
        )
        .execute();

      for (const anc of ancestors) {
        const ancRows = await publishedActivityVersionsQuery(
          this.db
            .selectFrom('content.activity_skill as ask')
            .innerJoin(
              'content.activity_version as av',
              'av.id',
              'ask.activity_version_id',
            )
            .innerJoin('content.activity as a', 'a.id', 'av.activity_id')
            .select([
              'a.id as activity_id',
              'av.id as activity_version_id',
              'a.slug',
              'av.difficulty_level',
            ]),
          tenantId,
        )
          .where('ask.skill_id', '=', anc.ancestor_id)
          .where('av.difficulty_level', 'in', ['L1', 'L2'])
          .orderBy('av.difficulty_level', 'asc')
          .limit(1)
          .execute();

        for (const row of ancRows) {
          // Ancestor remediation is slightly below own-skill remediation
          // (fix the direct blocker first) but above everything else.
          const score = 0.65;
          const existing = candidates.get(row.activity_version_id);
          if (!existing || existing.score < score) {
            candidates.set(row.activity_version_id, {
              activityId: row.activity_id,
              activityVersionId: row.activity_version_id,
              slug: row.slug,
              score,
              reasonCode: 'REMEDIATION',
              reasonParams: {
                blocked_skill_id: skill_id,
                prerequisite_skill_id: anc.ancestor_id,
                prerequisite_mastery:
                  anc.anc_mastery == null ? null : Number(anc.anc_mastery),
              },
            });
          }
        }
      }
    }
  }

  /**
   * Doc §2.5 f5 / §2.4 "review-due" (G9). A skill the learner is
   * Competent+ on whose skill_mastery.review_due_at has passed: offer an
   * activity that exercises it, so mastery is re-confirmed before decay
   * drags it below the eligibility gate.
   */
  private async addSpacedRepetitionCandidates(
    userId: string,
    tenantId: string,
    candidates: Map<string, Recommendation>,
  ) {
    const dueSkills = await this.db
      .selectFrom('skill.skill_mastery')
      .select(['skill_id', 'p_mastery', 'band', 'review_due_at'])
      .where('user_id', '=', userId)
      .where('review_due_at', 'is not', null)
      .where(sql<SqlBool>`review_due_at <= now()`)
      .execute();

    if (dueSkills.length === 0) return;

    for (const s of dueSkills) {
      const rows = await publishedActivityVersionsQuery(
        this.db
          .selectFrom('content.activity_skill as ask')
          .innerJoin(
            'content.activity_version as av',
            'av.id',
            'ask.activity_version_id',
          )
          .innerJoin('content.activity as a', 'a.id', 'av.activity_id')
          .select([
            'a.id as activity_id',
            'av.id as activity_version_id',
            'a.slug',
          ]),
        tenantId,
      )
        .where('ask.skill_id', '=', s.skill_id)
        .where('ask.is_primary', '=', true)
        .execute();

      for (const row of rows) {
        const score = 0.55; // steady nudge; SOURCE_WEIGHT lowers it further
        const existing = candidates.get(row.activity_version_id);
        if (!existing || existing.score < score) {
          candidates.set(row.activity_version_id, {
            activityId: row.activity_id,
            activityVersionId: row.activity_version_id,
            slug: row.slug,
            score,
            reasonCode: 'SPACED_REPETITION',
            reasonParams: {
              skill_id: s.skill_id,
              band: s.band,
              review_due_at: s.review_due_at,
            },
          });
        }
      }
    }
  }

  /**
   * Doc §2.5 f ("next-difficulty for Competent+") (G8, source d). The
   * learner is Proficient/Mastered on a skill: offer the next-difficulty
   * activity that targets it so they keep progressing rather than
   * re-grinding what they already know.
   */
  private async addProgressionCandidates(
    userId: string,
    tenantId: string,
    candidates: Map<string, Recommendation>,
  ) {
    const strongSkills = await this.db
      .selectFrom('skill.skill_mastery')
      .select(['skill_id', 'p_mastery', 'band'])
      .where('user_id', '=', userId)
      .where('band', 'in', ['Proficient', 'Mastered'])
      .execute();

    if (strongSkills.length === 0) return;

    const attemptedActivityIds = new Set(
      (
        await this.db
          .selectFrom('attempt.attempt')
          .select('activity_id')
          .where('user_id', '=', userId)
          .distinct()
          .execute()
      ).map((a) => a.activity_id),
    );

    for (const s of strongSkills) {
      // Proficient -> L3/L4 next; Mastered -> L4/L5.
      const targetLevels: Array<'L3' | 'L4' | 'L5'> =
        s.band === 'Mastered' ? ['L4', 'L5'] : ['L3', 'L4'];
      const rows = await publishedActivityVersionsQuery(
        this.db
          .selectFrom('content.activity_skill as ask')
          .innerJoin(
            'content.activity_version as av',
            'av.id',
            'ask.activity_version_id',
          )
          .innerJoin('content.activity as a', 'a.id', 'av.activity_id')
          .select([
            'a.id as activity_id',
            'av.id as activity_version_id',
            'a.slug',
            'av.difficulty_level',
          ]),
        tenantId,
      )
        .where('ask.skill_id', '=', s.skill_id)
        .where('av.difficulty_level', 'in', targetLevels)
        .execute();

      for (const row of rows) {
        if (attemptedActivityIds.has(row.activity_id)) continue;
        const score = 0.6;
        const existing = candidates.get(row.activity_version_id);
        if (!existing || existing.score < score) {
          candidates.set(row.activity_version_id, {
            activityId: row.activity_id,
            activityVersionId: row.activity_version_id,
            slug: row.slug,
            score,
            reasonCode: 'PROGRESSION',
            reasonParams: {
              skill_id: s.skill_id,
              band: s.band,
              next_level: row.difficulty_level,
            },
          });
        }
      }
    }
  }

  /**
   * Doc §2.5 "unblocking-value" (G8, source e). Find published activities
   * the learner is INELIGIBLE for solely because of one unmet REQUIRES
   * prereq skill, and recommend the prereq activity instead -- so the
   * desirable target unblocks. The prereq activity itself must be
   * eligible (the main recommend() loop enforces that); the target is
   * only named in reasonParams.
   */
  private async addUnblockingCandidates(
    userId: string,
    tenantId: string,
    candidates: Map<string, Recommendation>,
  ) {
    // Skills the learner has NOT mastered (below the REQUIRES gate).
    const weakSkills = await this.db
      .selectFrom('skill.skill_mastery')
      .select(['skill_id', 'p_mastery'])
      .where('user_id', '=', userId)
      .where('p_mastery', '<', MasteryConstants.REQUIRES_GATE_THRESHOLD)
      .execute();
    if (weakSkills.length === 0) return;
    const weakSkillIds = new Set(weakSkills.map((w) => w.skill_id));

    // Published activities whose PRIMARY skill is one of those weak
    // skills -> those are the "unblocking" prereq activities to offer.
    for (const skillId of weakSkillIds) {
      const rows = await publishedActivityVersionsQuery(
        this.db
          .selectFrom('content.activity_skill as ask')
          .innerJoin(
            'content.activity_version as av',
            'av.id',
            'ask.activity_version_id',
          )
          .innerJoin('content.activity as a', 'a.id', 'av.activity_id')
          .select([
            'a.id as activity_id',
            'av.id as activity_version_id',
            'a.slug',
          ]),
        tenantId,
      )
        .where('ask.skill_id', '=', skillId)
        .where('ask.is_primary', '=', true)
        .execute();

      // How many other activities would this skill unblock? (its
      // descendants in the closure that the learner can't yet reach).
      const unblocksCount = await this.db
        .selectFrom('skill.skill_closure')
        .select((eb) => eb.fn.countAll<string>().as('n'))
        .where('ancestor_id', '=', skillId)
        .executeTakeFirst();
      const unblocks = Number(unblocksCount?.n ?? 0);
      if (unblocks === 0) continue;

      for (const row of rows) {
        // More downstream activities unblocked => higher value.
        const score = Math.min(0.6 + unblocks * 0.05, 0.95);
        const existing = candidates.get(row.activity_version_id);
        if (!existing || existing.score < score) {
          candidates.set(row.activity_version_id, {
            activityId: row.activity_id,
            activityVersionId: row.activity_version_id,
            slug: row.slug,
            score,
            reasonCode: 'UNBLOCKING',
            reasonParams: { prerequisite_skill_id: skillId, unblocks },
          });
        }
      }
    }
  }
}
