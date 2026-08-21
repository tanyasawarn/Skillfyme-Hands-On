import { Inject, Injectable, ConflictException } from '@nestjs/common';
import type { Kysely, Transaction } from 'kysely';
import { KYSELY } from '../../db/database.module';
import type { ActivityVersionStatus, Database } from '../../db/schema';

export interface PublishActivityVersionInput {
  tenantId: string;
  activitySlug: string;
  mode: 'GUIDED_LAB' | 'PRODUCTION_SIM' | 'PROJECT';
  spec: {
    id: string;
    version: number;
    meta: { difficulty_level: string; estimated_minutes: number };
    environment: { blueprint: string; cost_budget_usd: number };
    skills: Array<{
      skill: string;
      weight: number;
      primary: boolean;
      bloom?: string;
    }>;
    curriculum?: { primary_topic?: string; also_relevant?: string[] };
  };
  publishedBy?: string;
}

@Injectable()
export class CatalogRepository {
  constructor(@Inject(KYSELY) private readonly db: Kysely<Database>) {}

  /**
   * Doc §3.6 rule 11: "Publishing creates a new version. Editing a
   * published version is impossible." This method only ever INSERTs a new
   * activity_version row — it never UPDATEs an existing one, so the DB
   * trigger (0001_curriculum_and_skills.sql) is defense-in-depth, not the
   * only guard. Skill weights must resolve against skill.skill by slug;
   * unresolvable slugs fail the whole publish transactionally (matches
   * the "skills exist" lint rule already enforced pre-publish by
   * SpecLintService, but re-checked here because DB state can drift
   * between lint time and publish time).
   */
  async publishNewVersion(input: PublishActivityVersionInput) {
    return this.db
      .transaction()
      .execute((trx) => this.insertVersion(trx, input, 'PUBLISHED'));
  }

  /**
   * Doc §3.7 CMS draft/preview/approval flow: creates a new version in
   * DRAFT status instead of publishing immediately -- everything else
   * (skill/topic binding, version-sequence check) is identical to
   * publishNewVersion, which is why both share insertVersion rather than
   * duplicating this transaction. A draft is invisible to every
   * learner-facing query (listPublishedCatalog etc. all filter on
   * status = 'PUBLISHED'), so creating one has zero effect on the live
   * catalog until it's explicitly moved through review -> approval ->
   * publish (see transitionVersionStatus below).
   */
  async createDraft(input: PublishActivityVersionInput) {
    return this.db
      .transaction()
      .execute((trx) => this.insertVersion(trx, input, 'DRAFT'));
  }

  private async insertVersion(
    trx: Transaction<Database>,
    input: PublishActivityVersionInput,
    status: ActivityVersionStatus,
  ) {
    let activity = await trx
      .selectFrom('content.activity')
      .selectAll()
      .where('tenant_id', '=', input.tenantId)
      .where('slug', '=', input.activitySlug)
      .executeTakeFirst();

    if (!activity) {
      activity = await trx
        .insertInto('content.activity')
        .values({
          tenant_id: input.tenantId,
          slug: input.activitySlug,
          mode: input.mode,
        })
        .returningAll()
        .executeTakeFirstOrThrow();
    }

    const latest = await trx
      .selectFrom('content.activity_version')
      .select('version')
      .where('activity_id', '=', activity.id)
      .orderBy('version', 'desc')
      .executeTakeFirst();

    const nextVersion = (latest?.version ?? 0) + 1;
    if (input.spec.version !== nextVersion) {
      throw new ConflictException(
        `spec declares version ${input.spec.version} but next available version for ${input.activitySlug} is ${nextVersion} — re-sync before publishing (doc §3.6)`,
      );
    }

    const skillRows = await trx
      .selectFrom('skill.skill')
      .select(['id', 'slug'])
      .where(
        'slug',
        'in',
        input.spec.skills.map((s) => s.skill),
      )
      .execute();
    const skillIdBySlug = new Map(skillRows.map((r) => [r.slug, r.id]));
    const missing = input.spec.skills.filter(
      (s) => !skillIdBySlug.has(s.skill),
    );
    if (missing.length > 0) {
      throw new ConflictException(
        `cannot publish: unknown skill slugs ${missing.map((m) => m.skill).join(', ')}`,
      );
    }

    const version = await trx
      .insertInto('content.activity_version')
      .values({
        activity_id: activity.id,
        version: nextVersion,
        status,
        spec_jsonb: input.spec as unknown as Record<string, never>,
        blueprint_id: input.spec.environment.blueprint,
        difficulty_level: input.spec.meta.difficulty_level as never,
        estimated_minutes: input.spec.meta.estimated_minutes,
        cost_budget_usd: input.spec.environment.cost_budget_usd,
        // published_at/published_by only make sense once status actually
        // reaches PUBLISHED -- a DRAFT row leaves both null, and the
        // publish step of transitionVersionStatus sets them for real.
        ...(status === 'PUBLISHED' ? { published_at: new Date() } : {}),
        ...(status === 'PUBLISHED' && input.publishedBy
          ? { published_by: input.publishedBy }
          : {}),
      })
      .returningAll()
      .executeTakeFirstOrThrow();

    for (const s of input.spec.skills) {
      await trx
        .insertInto('content.activity_skill')
        .values({
          activity_version_id: version.id,
          skill_id: skillIdBySlug.get(s.skill)!,
          weight: s.weight,
          is_primary: s.primary,
          bloom_level: s.bloom ?? null,
        })
        .execute();
    }

    // Doc §2.2 chain: activity binds to topics via curriculum.primary_topic
    // (relevance 1.0) and also_relevant (relevance 0.5, "prior/future"
    // weighting per §2.5's f3 CurriculumAlign feature -- exact weights
    // aren't specified for also_relevant in the doc, 0.5 is a reasonable
    // "relevant but not primary" default). Topics not yet seeded in
    // content.topic are skipped, not fatal -- unlike skills, curriculum
    // placement doesn't gate publishing (doc §2.1: the curriculum tree is
    // a marketing/packaging artifact, reshuffled independently of content
    // correctness).
    const topicSlugs = [
      ...(input.spec.curriculum?.primary_topic
        ? [{ slug: input.spec.curriculum.primary_topic, relevance: 1.0 }]
        : []),
      ...(input.spec.curriculum?.also_relevant ?? []).map((slug) => ({
        slug,
        relevance: 0.5,
      })),
    ];
    for (const { slug, relevance } of topicSlugs) {
      const topic = await trx
        .selectFrom('content.topic')
        .select('id')
        .where('slug', '=', slug)
        .executeTakeFirst();
      if (!topic) continue;
      await trx
        .insertInto('content.activity_topic')
        .values({
          activity_version_id: version.id,
          topic_id: topic.id,
          relevance,
        })
        .execute();
    }

    return version;
  }

  /**
   * Doc §3.6/§3.7 lifecycle: DRAFT -> IN_REVIEW -> APPROVED -> PUBLISHED,
   * one forward step at a time -- `from` is required (not inferred) so a
   * caller can't accidentally skip a step by racing two transition
   * requests; a version whose current status doesn't match `from` fails
   * with a conflict rather than silently applying anyway. PUBLISHED is
   * the only step that also stamps published_at/published_by, matching
   * what a version reaching PUBLISHED via publishNewVersion's direct
   * path already gets.
   */
  private static readonly ALLOWED_TRANSITIONS: Record<
    ActivityVersionStatus,
    ActivityVersionStatus[]
  > = {
    DRAFT: ['IN_REVIEW'],
    IN_REVIEW: ['APPROVED', 'DRAFT'], // DRAFT: reviewer sends it back for changes
    APPROVED: ['PUBLISHED', 'DRAFT'], // DRAFT: approved-but-not-yet-published content can still be pulled back
    PUBLISHED: [],
    CANARY: [],
    DEPRECATED: [],
    RETIRED: [],
    ROLLED_BACK: [],
  };

  async transitionVersionStatus(
    activityVersionId: string,
    from: ActivityVersionStatus,
    to: ActivityVersionStatus,
    actor?: string,
  ) {
    const allowed = CatalogRepository.ALLOWED_TRANSITIONS[from] ?? [];
    if (!allowed.includes(to)) {
      throw new ConflictException(`cannot transition from ${from} to ${to}`);
    }

    return this.db.transaction().execute(async (trx) => {
      const current = await trx
        .selectFrom('content.activity_version')
        .select(['id', 'status'])
        .where('id', '=', activityVersionId)
        .executeTakeFirst();
      if (!current) {
        throw new ConflictException(
          `activity version ${activityVersionId} not found`,
        );
      }
      if (current.status !== from) {
        // Someone else already moved this version (or it was never in
        // the state the caller thought) -- a stale CMS tab retrying a
        // transition must not silently double-apply or skip a step.
        throw new ConflictException(
          `activity version ${activityVersionId} is ${current.status}, not ${from} -- refresh and retry`,
        );
      }

      return trx
        .updateTable('content.activity_version')
        .set({
          status: to,
          ...(to === 'PUBLISHED'
            ? {
                published_at: new Date(),
                ...(actor ? { published_by: actor } : {}),
              }
            : {}),
        })
        .where('id', '=', activityVersionId)
        .returningAll()
        .executeTakeFirstOrThrow();
    });
  }

  async getVersionById(activityVersionId: string) {
    return this.db
      .selectFrom('content.activity_version as av')
      .innerJoin('content.activity as a', 'a.id', 'av.activity_id')
      .select([
        'a.id as activity_id',
        'a.slug',
        'a.mode',
        'av.id as activity_version_id',
        'av.version',
        'av.status',
        'av.spec_jsonb',
        'av.difficulty_level',
        'av.estimated_minutes',
        'av.cost_budget_usd',
      ])
      .where('av.id', '=', activityVersionId)
      .executeTakeFirst();
  }

  async getPublishedVersion(activityId: string) {
    return this.db
      .selectFrom('content.activity_version')
      .selectAll()
      .where('activity_id', '=', activityId)
      .where('status', '=', 'PUBLISHED')
      .orderBy('version', 'desc')
      .executeTakeFirst();
  }

  async listPublishedCatalog(tenantId: string) {
    return this.db
      .selectFrom('content.activity_version as av')
      .innerJoin('content.activity as a', 'a.id', 'av.activity_id')
      .select([
        'a.id as activity_id',
        'a.slug',
        'a.mode',
        'av.id as activity_version_id',
        'av.version',
        'av.difficulty_level',
        'av.estimated_minutes',
        'av.cost_budget_usd',
      ])
      .where('a.tenant_id', '=', tenantId)
      .where('av.status', '=', 'PUBLISHED')
      .execute();
  }

  /**
   * Skill-driven catalog entry point: every skill in the graph for ONE
   * course, left-joined to whichever PUBLISHED activity (if any) lists
   * it as a primary skill for this tenant. Skills with no matching
   * activity come back with activity fields null -- the frontend renders
   * those as disabled ("coming soon") rather than omitting them, so the
   * catalog honestly reflects the full curriculum graph even where lab
   * content doesn't exist yet.
   *
   * courseSlug is required, not optional: this app is launched from an
   * external LMS already scoped to one course per session (URL param on
   * launch, e.g. /catalog?course=genai-with-ml) -- there is no in-app
   * course switcher, and returning skills from every course in one
   * unscoped list would mix two curricula's domains together (both
   * courses can legitimately have a domain literally named "core").
   */
  async listSkillDrivenCatalog(tenantId: string, courseSlug: string) {
    return (
      this.db
        .selectFrom('skill.skill as s')
        .leftJoin('content.activity_skill as asx', (join) =>
          join
            .onRef('asx.skill_id', '=', 's.id')
            .on('asx.is_primary', '=', true),
        )
        .leftJoin('content.activity_version as av', (join) =>
          join
            .onRef('av.id', '=', 'asx.activity_version_id')
            .on('av.status', '=', 'PUBLISHED'),
        )
        .leftJoin('content.activity as a', (join) =>
          join
            .onRef('a.id', '=', 'av.activity_id')
            .on('a.tenant_id', '=', tenantId),
        )
        .select([
          's.id as skill_id',
          's.slug as skill_slug',
          's.name as skill_name',
          's.domain as skill_domain',
          'a.id as activity_id',
          'a.slug as activity_slug',
          'a.mode as activity_mode',
          'av.id as activity_version_id',
          'av.version as activity_version',
          'av.difficulty_level',
          'av.estimated_minutes',
          'av.cost_budget_usd',
        ])
        .where('s.status', '=', 'active')
        .where('s.course_slug', '=', courseSlug)
        // a left-joined activity_version only counts if it actually resolved
        // for THIS tenant -- an activity published under a different tenant
        // must not silently mark the skill "available".
        .where((eb) =>
          eb.or([eb('a.id', 'is', null), eb('a.tenant_id', '=', tenantId)]),
        )
        // Curriculum teaching order, not alphabetical: domain_order ranks
        // the modules 1-N in this course's own sequence, sequence ranks
        // skills within a module. Sorting by s.domain/s.name text put
        // "DevOps with AI" above "DevOps Core Concepts" -- 'aiops' < 'core'
        // as text.
        .orderBy('s.domain_order')
        .orderBy('s.sequence')
        .execute()
    );
  }
}
