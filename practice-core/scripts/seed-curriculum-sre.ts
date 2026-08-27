/**
 * PLAN.md Phase 2: "Second course track content authoring." Seeds
 * course.sre's Course->Module->Topic structure and the topic_skill
 * mappings the SRE activities (content/activities/lab.sre.*.yaml,
 * sim.sre.*.yaml) reference via curriculum.primary_topic.
 *
 * Mirrors seed-curriculum.ts's shape (one course, its own modules/topics)
 * but topic_skill mappings deliberately point at BOTH the new SRE-only
 * skills (seed-skills-sre.ts) and pre-existing DevOps-track skills
 * (k8s.troubleshooting, observability.alerting, k8s.autoscaling) --
 * exactly the doc §2.1/D5 "skills shared across courses" reuse this
 * whole two-script split exists to enable. Run seed-skills.ts, then
 * seed-skills-sre.ts, then this script, in that order.
 *
 * Run with: npx ts-node -r tsconfig-paths/register scripts/seed-curriculum-sre.ts
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
    slug: 'course.sre',
    title: 'Site Reliability Engineering',
  });
  console.log(`Course: ${course.title} (${course.id})`);

  const incidentModule = await curriculum.createModule({
    courseId: course.id,
    title: 'Incident Response',
    position: 1,
    slug: 'module.sre.incident-response',
  });
  console.log(`Module: ${incidentModule.title} (${incidentModule.id})`);

  const diagnosisTopic = await curriculum.createTopic({
    moduleId: incidentModule.id,
    title: 'Diagnosing Production Incidents',
    position: 1,
    slug: 'topic.sre.incident-diagnosis',
  });
  console.log(`Topic: ${diagnosisTopic.title} (${diagnosisTopic.id})`);
  await curriculum.createSubtopic({ topicId: diagnosisTopic.id, title: 'Reading symptoms before acting', position: 1 });

  const postmortemTopic = await curriculum.createTopic({
    moduleId: incidentModule.id,
    title: 'Postmortems and Prevention',
    position: 2,
    slug: 'topic.sre.postmortems',
  });
  console.log(`Topic: ${postmortemTopic.title} (${postmortemTopic.id})`);
  await curriculum.createSubtopic({ topicId: postmortemTopic.id, title: 'Root cause, remediation, prevention', position: 1 });

  const reliabilityModule = await curriculum.createModule({
    courseId: course.id,
    title: 'Reliability Engineering',
    position: 2,
    slug: 'module.sre.reliability',
  });
  console.log(`Module: ${reliabilityModule.title} (${reliabilityModule.id})`);

  const sloTopic = await curriculum.createTopic({
    moduleId: reliabilityModule.id,
    title: 'SLOs and Error Budgets',
    position: 1,
    slug: 'topic.sre.slo-error-budgets',
  });
  console.log(`Topic: ${sloTopic.title} (${sloTopic.id})`);
  await curriculum.createSubtopic({ topicId: sloTopic.id, title: 'Burn rate and budget-gated risk', position: 1 });

  const capacityTopic = await curriculum.createTopic({
    moduleId: reliabilityModule.id,
    title: 'Capacity and Load',
    position: 2,
    slug: 'topic.sre.capacity-and-load',
  });
  console.log(`Topic: ${capacityTopic.title} (${capacityTopic.id})`);
  await curriculum.createSubtopic({ topicId: capacityTopic.id, title: 'Sizing under sustained load', position: 1 });

  const skills = await db.selectFrom('skill.skill').select(['id', 'slug']).execute();
  const skillIdBySlug = new Map(skills.map((s) => [s.slug, s.id]));

  async function mapSkill(topicId: string, slug: string, coverageWeight: number, bloomLevel: string) {
    const skillId = skillIdBySlug.get(slug);
    if (!skillId) {
      console.warn(`  ! skipping topic_skill mapping for unknown skill slug: ${slug} (has seed-skills.ts / seed-skills-sre.ts run first?)`);
      return;
    }
    await curriculum.mapTopicToSkill({ topicId, skillId, coverageWeight, bloomLevel });
    console.log(`  topic_skill: ${slug} -> weight ${coverageWeight}`);
  }

  // Diagnosis topic: the SRE-specific process skill is primary, but it
  // sits directly on top of the DevOps-track troubleshooting skill --
  // both get real coverage weight, not just a REQUIRES edge, because an
  // SRE incident-diagnosis activity genuinely exercises both.
  await mapSkill(diagnosisTopic.id, 'sre.incident-response-process', 0.6, 'apply');
  await mapSkill(diagnosisTopic.id, 'k8s.troubleshooting', 0.4, 'analyze');

  await mapSkill(postmortemTopic.id, 'sre.postmortem-authorship', 1.0, 'create');

  await mapSkill(sloTopic.id, 'sre.slo-error-budgets', 0.7, 'apply');
  await mapSkill(sloTopic.id, 'observability.alerting', 0.3, 'apply');

  await mapSkill(capacityTopic.id, 'sre.capacity-and-load', 0.6, 'apply');
  await mapSkill(capacityTopic.id, 'k8s.autoscaling', 0.4, 'apply');

  console.log('Done seeding course.sre curriculum.');
  await db.destroy();
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
