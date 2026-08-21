/**
 * Doc §2.2 worked example, seeded as real data: Course "DevOps With AI" ->
 * Module "Kubernetes" -> Topic "Deployments" -> Subtopic "Rolling updates
 * & rollbacks", with topic_skill mappings to the skills already used by
 * content/activities/lab.k8s.deploy-node-app.yaml so that activity's
 * curriculum.primary_topic ("topic.devops.k8s.deployments") resolves to a
 * real row when CatalogRepository.publishNewVersion runs.
 *
 * Run with: npx ts-node -r tsconfig-paths/register scripts/seed-curriculum.ts
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
    slug: 'course.devops-with-ai',
    title: 'DevOps With AI',
  });
  console.log(`Course: ${course.title} (${course.id})`);

  const k8sModule = await curriculum.createModule({ courseId: course.id, title: 'Kubernetes', position: 1, slug: 'module.devops.kubernetes' });
  console.log(`Module: ${k8sModule.title} (${k8sModule.id})`);

  const deploymentsTopic = await curriculum.createTopic({
    moduleId: k8sModule.id,
    title: 'Deployments',
    position: 1,
    slug: 'topic.devops.k8s.deployments',
  });
  console.log(`Topic: ${deploymentsTopic.title} (${deploymentsTopic.id})`);

  await curriculum.createSubtopic({ topicId: deploymentsTopic.id, title: 'Rolling updates & rollbacks', position: 1 });

  const containerTopic = await curriculum.createTopic({
    moduleId: k8sModule.id,
    title: 'Container Orchestration',
    position: 2,
    slug: 'topic.cloud.container-orchestration',
  });
  console.log(`Topic: ${containerTopic.title} (${containerTopic.id})`);

  const skills = await db.selectFrom('skill.skill').select(['id', 'slug']).execute();
  const skillIdBySlug = new Map(skills.map((s) => [s.slug, s.id]));

  // Doc §2.2 exact weights for skill:k8s.deployments's topic_skill mapping.
  const mappings: Array<{ slug: string; weight: number; bloom: string }> = [
    { slug: 'k8s.deployments', weight: 0.45, bloom: 'apply' },
    { slug: 'k8s.architecture', weight: 0.2, bloom: 'apply' },
    { slug: 'k8s.pods', weight: 0.2, bloom: 'apply' },
    { slug: 'k8s.services', weight: 0.15, bloom: 'understand' },
  ];
  for (const m of mappings) {
    const skillId = skillIdBySlug.get(m.slug);
    if (!skillId) {
      console.warn(`skipping topic_skill mapping for unknown skill slug: ${m.slug}`);
      continue;
    }
    await curriculum.mapTopicToSkill({ topicId: deploymentsTopic.id, skillId, coverageWeight: m.weight, bloomLevel: m.bloom });
  }
  console.log('Seeded topic_skill mappings.');

  await db.destroy();
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
