/**
 * PLAN.md Phase 2: "Second course track content authoring." Doc §2.1's
 * own overlap note is the design brief here: "Nine courses in the brief
 * overlap heavily (DevOps / SRE / Cloud / MLOps share 60%+ of skills)...
 * Modelling prerequisites in the curriculum tree means authoring the
 * same prerequisite nine times and drifting nine ways." This script
 * therefore does NOT re-author the ~120 skills seed-skills.ts already
 * seeded for course.devops-with-ai (k8s.*, observability.*, iac.*,
 * cloud.* are directly reusable -- an SRE learner needs the same
 * kubectl-troubleshooting and Prometheus skills a DevOps learner does).
 * It adds only the skills genuinely specific to the SRE discipline that
 * have no DevOps-track equivalent: SLO/error-budget mechanics, the
 * incident-response process itself (not just the tooling), and
 * postmortem authorship -- the material doc §1.3.2's Production
 * Implementation mode and this session's InjectFault/blast_radius/
 * process-signal work already exist to exercise, but that no existing
 * skill row names.
 *
 * Deliberately a SEPARATE script from seed-skills.ts, not an addition to
 * it -- that script's own doc comment scopes it to "Skillfyme's 'DevOps
 * with AI' curriculum... does not invent skills outside that scope."
 * Adding SRE-specific skills there would violate that stated contract;
 * a parallel script keeps each course's skill provenance traceable to
 * its own curriculum source.
 *
 * Run with: npx ts-node -r tsconfig-paths/register scripts/seed-skills-sre.ts
 * (after seed-skills.ts -- this script's REQUIRES edges reference
 * DevOps-track skill slugs like k8s.troubleshooting that must already
 * exist)
 */
import { Kysely, PostgresDialect } from 'kysely';
import { Pool } from 'pg';
import type { Database } from '../src/db/schema';
import { SkillRepository } from '../src/modules/skill/skill.repository';

interface SkillDef {
  slug: string;
  name: string;
  domain: string;
  description: string;
}

interface EdgeDef {
  from: string;
  to: string;
}

// New domain, sequenced after every DevOps-track domain (seed-skills.ts's
// DOMAIN_ORDER tops out at aiops: 7) -- these are SRE-specific
// capabilities that build on, not replace, the shared K8s/observability
// foundation.
const SRE_DOMAIN_ORDER = 8;

const SRE_SKILLS: SkillDef[] = [
  {
    slug: 'sre.slo-error-budgets',
    name: 'SLOs and Error Budgets',
    domain: 'sre',
    description:
      'Defining SLIs/SLOs, computing error budgets, and using budget burn rate to gate risk (deploy freezes, prioritisation) -- the quantitative half of doc §1.3.2\'s "business impact" ticket framing.',
  },
  {
    slug: 'sre.incident-response-process',
    name: 'Incident Response Process',
    domain: 'sre',
    description:
      'Severity classification, escalation, and structured on-call response during a live incident -- the process discipline around the diagnostic skills k8s.troubleshooting/observability.* already cover, not a replacement for them.',
  },
  {
    slug: 'sre.postmortem-authorship',
    name: 'Blameless Postmortem Authorship',
    domain: 'sre',
    description:
      'Writing a root-cause/remediation/prevention incident note per a fixed rubric -- doc §6.5\'s rub.incident-note.v2, first AI-graded artifact type, is the assessment mechanism for this exact skill.',
  },
  {
    slug: 'sre.capacity-and-load',
    name: 'Capacity Planning Under Load',
    domain: 'sre',
    description:
      'Reading load/latency signals to size replica counts and autoscaling policy correctly -- pairs with the existing k8s.autoscaling skill but focused on the SRE lens (SLO-driven capacity, not just "how HPA works").',
  },
];

// REQUIRES edges: SRE-specific skills build ON existing DevOps-track
// skills (cross-domain edges are exactly what doc §2.1's shared-skill-
// graph design is for), plus internal SRE sequencing.
const EDGES: EdgeDef[] = [
  // Cross-domain: SRE skills require the DevOps-track foundation they sit on top of.
  { from: 'k8s.troubleshooting', to: 'sre.incident-response-process' },
  { from: 'observability.alerting', to: 'sre.slo-error-budgets' },
  { from: 'k8s.autoscaling', to: 'sre.capacity-and-load' },
  // Internal SRE sequence: you classify/respond to an incident before writing it up.
  { from: 'sre.incident-response-process', to: 'sre.postmortem-authorship' },
  // Error-budget literacy informs how a postmortem frames business impact.
  { from: 'sre.slo-error-budgets', to: 'sre.postmortem-authorship' },
];

async function main() {
  const db = new Kysely<Database>({
    dialect: new PostgresDialect({
      pool: new Pool({
        connectionString: process.env.DATABASE_URL ?? 'postgres://practice:practice@localhost:5433/practice_engine',
      }),
    }),
  });

  const skills = new SkillRepository(db);

  console.log(`Inserting ${SRE_SKILLS.length} SRE-track skills...`);
  const idBySlug = new Map<string, string>();
  let sequence = 0;
  for (const s of SRE_SKILLS) {
    sequence += 1;
    const row = await skills.createSkill({
      slug: s.slug,
      name: s.name,
      domain: s.domain,
      description: s.description,
      domainOrder: SRE_DOMAIN_ORDER,
      sequence,
      // KNOWN GAP, not fixed by this pass: skill.skill.course_slug
      // (migration 0005_skill_course_scope.sql) is a single-value
      // column, not the doc §2.1/D5 many-to-many "skills shared across
      // courses" model -- it was added for an unrelated "GenAI with ML"
      // course, scoping each skill's *mastery-dashboard listing* to
      // exactly one course. Setting it to 'sre' here means these 4
      // skills show up on an SRE learner's mastery dashboard
      // (skill.controller.ts's ?course=sre query), but the shared
      // DevOps-track skills SRE activities also use (k8s.troubleshooting,
      // observability.alerting, k8s.autoscaling) stay course_slug=
      // 'devops-with-ai' and will NOT appear in that same dashboard
      // listing for an SRE learner, even though BKT mastery evidence on
      // them updates normally (mastery.service.ts's evidence-update path
      // isn't course-scoped -- only listMasteryForUser's dashboard query
      // is). Real fix is a skill_course join table or an array column;
      // out of scope for authoring SRE content, flagged here rather than
      // silently worked around.
      courseSlug: 'sre',
    });
    idBySlug.set(s.slug, row.id);
    console.log(`  + ${s.slug}`);
  }

  console.log(`Inserting ${EDGES.length} REQUIRES edges (including cross-domain links into the existing DevOps skill graph)...`);
  // Re-query rather than reuse idBySlug alone -- EDGES references both
  // the SRE skills just inserted above AND pre-existing DevOps-track
  // skills (k8s.troubleshooting, observability.alerting, k8s.autoscaling)
  // that idBySlug never had entries for.
  const existingSkills = await db.selectFrom('skill.skill').select(['id', 'slug']).execute();
  const allIdBySlug = new Map(existingSkills.map((s) => [s.slug, s.id]));
  for (const e of EDGES) {
    const fromId = allIdBySlug.get(e.from);
    const toId = allIdBySlug.get(e.to);
    if (!fromId || !toId) {
      console.warn(`  ! skipping edge ${e.from} -> ${e.to}: unknown slug (has seed-skills.ts run first?)`);
      continue;
    }
    await skills.addEdge({ fromSkillId: fromId, toSkillId: toId, type: 'REQUIRES' });
  }

  console.log('Rebuilding skill.skill_closure...');
  await skills.rebuildClosure();

  console.log(`Done: ${SRE_SKILLS.length} SRE skills, ${EDGES.length} edges seeded.`);
  await db.destroy();
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
