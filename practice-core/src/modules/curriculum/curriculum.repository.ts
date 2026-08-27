import { Injectable } from '@nestjs/common';
import { BaseRepository } from '../../common/base.repository';

/**
 * Doc §2.1 (D5): "Maintain a Curriculum Hierarchy and a Skill Graph as
 * separate structures, joined only through activities and through an
 * explicit topic_skill mapping table." This repository owns only the
 * curriculum side (Course->Module->Topic->Subtopic + topic_skill); it
 * never writes to skill.* tables -- SkillRepository (existing) is the
 * only writer there, per the doc's "never conflate them" rule.
 */
@Injectable()
export class CurriculumRepository extends BaseRepository {
  async createCourse(input: { tenantId: string; slug: string; title: string }) {
    return this.db
      .insertInto('content.course')
      .values({
        tenant_id: input.tenantId,
        slug: input.slug,
        title: input.title,
        status: 'active',
      })
      .onConflict((oc) =>
        oc.columns(['tenant_id', 'slug']).doUpdateSet({ title: input.title }),
      )
      .returningAll()
      .executeTakeFirstOrThrow();
  }

  async createModule(input: {
    courseId: string;
    title: string;
    position: number;
    slug?: string;
  }) {
    return this.db
      .insertInto('content.module')
      .values({
        course_id: input.courseId,
        title: input.title,
        position: input.position,
        slug: input.slug ?? null,
      })
      .returningAll()
      .executeTakeFirstOrThrow();
  }

  async createTopic(input: {
    moduleId: string;
    title: string;
    position: number;
    slug?: string;
  }) {
    return this.db
      .insertInto('content.topic')
      .values({
        module_id: input.moduleId,
        title: input.title,
        position: input.position,
        slug: input.slug ?? null,
      })
      .returningAll()
      .executeTakeFirstOrThrow();
  }

  /** Doc §3.2: activity spec's curriculum.primary_topic/also_relevant reference topics by this slug. */
  async findTopicBySlug(slug: string) {
    return this.db
      .selectFrom('content.topic')
      .selectAll()
      .where('slug', '=', slug)
      .executeTakeFirst();
  }

  async createSubtopic(input: {
    topicId: string;
    title: string;
    position: number;
  }) {
    return this.db
      .insertInto('content.subtopic')
      .values({
        topic_id: input.topicId,
        title: input.title,
        position: input.position,
      })
      .returningAll()
      .executeTakeFirstOrThrow();
  }

  /** Doc §2.2: topic_skill(topic_id, skill_id, coverage_weight, bloom_level). */
  async mapTopicToSkill(input: {
    topicId: string;
    skillId: string;
    coverageWeight: number;
    bloomLevel: string;
  }) {
    await this.db
      .insertInto('content.topic_skill')
      .values({
        topic_id: input.topicId,
        skill_id: input.skillId,
        coverage_weight: input.coverageWeight,
        bloom_level: input.bloomLevel,
      })
      .onConflict((oc) =>
        oc.columns(['topic_id', 'skill_id']).doUpdateSet({
          coverage_weight: input.coverageWeight,
          bloom_level: input.bloomLevel,
        }),
      )
      .execute();
  }

  /** Full tree for one course, doc §1.2/§1.3: what the catalog's course/topic filters walk. */
  async getCourseTree(tenantId: string, courseSlug: string) {
    const course = await this.db
      .selectFrom('content.course')
      .selectAll()
      .where('tenant_id', '=', tenantId)
      .where('slug', '=', courseSlug)
      .executeTakeFirst();
    if (!course) return null;

    const modules = await this.db
      .selectFrom('content.module')
      .selectAll()
      .where('course_id', '=', course.id)
      .orderBy('position')
      .execute();

    const moduleIds = modules.map((m) => m.id);
    const topics = moduleIds.length
      ? await this.db
          .selectFrom('content.topic')
          .selectAll()
          .where('module_id', 'in', moduleIds)
          .orderBy('position')
          .execute()
      : [];

    const topicIds = topics.map((t) => t.id);
    const subtopics = topicIds.length
      ? await this.db
          .selectFrom('content.subtopic')
          .selectAll()
          .where('topic_id', 'in', topicIds)
          .orderBy('position')
          .execute()
      : [];

    const topicSkills = topicIds.length
      ? await this.db
          .selectFrom('content.topic_skill as ts')
          .innerJoin('skill.skill as s', 's.id', 'ts.skill_id')
          .select([
            'ts.topic_id',
            's.slug as skill_slug',
            's.name as skill_name',
            'ts.coverage_weight',
            'ts.bloom_level',
          ])
          .where('ts.topic_id', 'in', topicIds)
          .execute()
      : [];

    return {
      course,
      modules: modules.map((m) => ({
        ...m,
        topics: topics
          .filter((t) => t.module_id === m.id)
          .map((t) => ({
            ...t,
            subtopics: subtopics.filter((s) => s.topic_id === t.id),
            skills: topicSkills.filter((ts) => ts.topic_id === t.id),
          })),
      })),
    };
  }

  async listCourses(tenantId: string) {
    return this.db
      .selectFrom('content.course')
      .selectAll()
      .where('tenant_id', '=', tenantId)
      .execute();
  }

  /** Doc §1.2: catalog filtered by course -- resolves an activity's topic(s) back to their course(s). */
  async listCoursesForActivityVersion(activityVersionId: string) {
    return this.db
      .selectFrom('content.activity_topic as at')
      .innerJoin('content.topic as t', 't.id', 'at.topic_id')
      .innerJoin('content.module as m', 'm.id', 't.module_id')
      .innerJoin('content.course as c', 'c.id', 'm.course_id')
      .select(['c.id as course_id', 'c.slug', 'c.title'])
      .where('at.activity_version_id', '=', activityVersionId)
      .distinct()
      .execute();
  }
}
