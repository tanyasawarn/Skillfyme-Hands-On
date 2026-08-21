/**
 * Fixes a real gap found in audit: skill.skill.course_slug='genai-with-ml'
 * has 53 skills, but content.course had zero rows for this course --
 * GET /v1/practice/courses/genai-with-ml would 404 even though the
 * skill-driven catalog (which reads skill.skill directly, not
 * content.course) already works. Mirrors seed-curriculum.ts's scope
 * exactly: one course row + one worked-example module/topic pairing (not
 * the full 7-module tree -- the DevOps course itself only seeded one
 * module as a worked example too), with topic_skill mappings for the one
 * published GenAI activity once one exists.
 *
 * Run with: npx ts-node -r tsconfig-paths/register scripts/seed-curriculum-genai.ts
 */
import { Kysely, PostgresDialect } from 'kysely';
import { Pool } from 'pg';
import type { Database } from '../src/db/schema';
import { CurriculumRepository } from '../src/modules/curriculum/curriculum.repository';

const DEMO_TENANT_ID = '11111111-1111-1111-1111-111111111111';

async function main() {
  const db = new Kysely<Database>({
    dialect: new PostgresDialect({
      pool: new Pool({
        connectionString: process.env.DATABASE_URL ?? 'postgres://practice:practice@localhost:5433/practice_engine',
      }),
    }),
  });

  const curriculum = new CurriculumRepository(db);

  const course = await curriculum.createCourse({
    tenantId: DEMO_TENANT_ID,
    slug: 'course.genai-with-ml',
    title: 'Generative AI With ML',
  });
  console.log(`Course: ${course.title} (${course.id})`);

  const pythonModule = await curriculum.createModule({
    courseId: course.id,
    title: 'Python & Statistics Essentials',
    position: 1,
    slug: 'module.genai.python-stats',
  });
  console.log(`Module: ${pythonModule.title} (${pythonModule.id})`);

  const fundamentalsTopic = await curriculum.createTopic({
    moduleId: pythonModule.id,
    title: 'Python Programming Fundamentals',
    position: 1,
    slug: 'topic.genai.python-fundamentals',
  });
  console.log(`Topic: ${fundamentalsTopic.title} (${fundamentalsTopic.id})`);

  await curriculum.createSubtopic({ topicId: fundamentalsTopic.id, title: 'Syntax, data types & interactive coding', position: 1 });

  const dataAnalysisTopic = await curriculum.createTopic({
    moduleId: pythonModule.id,
    title: 'Data Analysis & Visualization',
    position: 2,
    slug: 'topic.genai.data-analysis-visualization',
  });
  console.log(`Topic: ${dataAnalysisTopic.title} (${dataAnalysisTopic.id})`);

  const skills = await db
    .selectFrom('skill.skill')
    .select(['id', 'slug'])
    .where('course_slug', '=', 'genai-with-ml')
    .execute();
  const skillIdBySlug = new Map(skills.map((s) => [s.slug, s.id]));

  const mappings: Array<{ slug: string; weight: number; bloom: string }> = [
    { slug: 'python.fundamentals', weight: 0.4, bloom: 'apply' },
    { slug: 'python.data-structures-control-flow', weight: 0.3, bloom: 'apply' },
    { slug: 'python.functions-oop-exceptions', weight: 0.3, bloom: 'apply' },
  ];
  for (const m of mappings) {
    const skillId = skillIdBySlug.get(m.slug);
    if (!skillId) {
      console.warn(`skipping topic_skill mapping for unknown skill slug: ${m.slug}`);
      continue;
    }
    await curriculum.mapTopicToSkill({ topicId: fundamentalsTopic.id, skillId, coverageWeight: m.weight, bloomLevel: m.bloom });
  }
  console.log('Seeded topic_skill mappings.');

  await db.destroy();
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
