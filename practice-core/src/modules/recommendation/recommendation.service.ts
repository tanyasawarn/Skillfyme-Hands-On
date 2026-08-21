import { Inject, Injectable } from '@nestjs/common';
import { sql, type Kysely, type SqlBool } from 'kysely';
import { KYSELY } from '../../db/database.module';
import type { Database } from '../../db/schema';
import { EligibilityService } from '../attempt/eligibility.service';

export interface Recommendation {
  activityId: string;
  activityVersionId: string;
  slug: string;
  score: number;
  reasonCode: 'CURRICULUM_ADJACENT' | 'REMEDIATION';
  reasonParams: Record<string, unknown>;
}

/**
 * Doc §2.5 four-stage pipeline, reduced to Phase 1 scope per PLAN.md
 * M1.11: "Candidate gen (curriculum-adjacent + remediation only) ->
 * eligibility filter -> simple scoring, no ML." The doc's other three
 * candidate sources (spaced repetition, progression, unblocking-value)
 * need data this system doesn't have yet: review-due dates need mastery
 * history older than this session can produce, and "next-difficulty for
 * Competent+" needs a mastery distribution across many skills to be
 * meaningful. Curriculum-adjacent and remediation are the two sources
 * that work with a cold-start learner and the curriculum/mastery data
 * already built (M1.5, M1.10).
 *
 * Doc §2.5: "Every recommendation must carry a human-readable reason...
 * this is not cosmetic -- it is how you debug the recommender, how
 * learners trust it." reasonCode + reasonParams are structured, not
 * pre-rendered strings, so the frontend renders the copy (keeps English
 * text out of the backend, matches the doc's "structured data" note).
 */
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

    const filtered: Recommendation[] = [];
    for (const candidate of candidates.values()) {
      const result = await this.eligibility.check(
        userId,
        candidate.activityVersionId,
      );
      if (result.eligible) filtered.push(candidate);
    }

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
            ranker_version: 'rules-v1',
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

    const rows = await this.db
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
      ])
      .where('a.tenant_id', '=', tenantId)
      .where('av.status', '=', 'PUBLISHED')
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
      .where('sm.p_mastery', '<', 0.55)
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
      const rows = await this.db
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
        ])
        .where('ask.skill_id', '=', skill_id)
        .where('a.tenant_id', '=', tenantId)
        .where('av.status', '=', 'PUBLISHED')
        .where('av.difficulty_level', 'in', ['L1', 'L2'])
        .execute();

      for (const row of rows) {
        const score = 0.7 + (0.55 - Number(p_mastery)); // lower mastery -> higher urgency
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
    }
  }
}
