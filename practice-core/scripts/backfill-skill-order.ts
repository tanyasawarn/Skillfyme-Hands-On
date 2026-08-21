/**
 * One-off backfill: sets domain_order/sequence on skills already inserted
 * by seed-skills.ts before the 0004_skill_sequence.sql migration existed.
 * Re-running seed-skills.ts isn't an option (createSkill isn't idempotent,
 * unique slug constraint) -- this updates existing rows by slug instead,
 * using the identical curriculum ordering data seed-skills.ts already
 * defines, so there is exactly one source of truth for the sequence.
 *
 * Run with: npx ts-node -r tsconfig-paths/register scripts/backfill-skill-order.ts
 */
import { Kysely, PostgresDialect } from 'kysely';
import { Pool } from 'pg';
import type { Database } from '../src/db/schema';

const DOMAIN_ORDER: Record<string, number> = {
  core: 1,
  cicd: 2,
  containers: 3,
  cloud: 4,
  iac: 5,
  observability: 6,
  aiops: 7,
};

// Curriculum order, copied from seed-skills.ts's SKILLS concatenation --
// kept as a flat slug list here since importing the .ts module's arrays
// directly is simpler than re-deriving them from the DB.
const SLUG_ORDER: string[] = [
  'devops.fundamentals', 'linux.cli', 'devops.gitops-evolution', 'iac.fundamentals',
  'microservices.fundamentals', 'microservices.devops-impact', 'devsecops.fundamentals',
  'devsecops.container-security-tools', 'iac.terraform-vs-cloudformation', 'git.basics',
  'git.branching-strategies', 'git.internals', 'git.workflow-patterns', 'git.release-management',
  'jenkins.basics', 'jenkins.pipeline-as-code', 'jenkins.security-integration',
  'jenkins.distributed-builds', 'jenkins.advanced-pipelines', 'gitlab.cicd-pipelines',
  'github.actions-workflows', 'cicd.troubleshooting',
  'docker.basics', 'docker.networking', 'docker.swarm', 'k8s.architecture', 'k8s.pods',
  'k8s.deployments', 'k8s.services', 'k8s.config-secrets', 'k8s.helm', 'k8s.statefulsets',
  'k8s.storage', 'k8s.scheduling', 'k8s.resource-management', 'k8s.production-deployments',
  'k8s.autoscaling', 'k8s.troubleshooting', 'istio.traffic-management', 'istio.mtls-security',
  'tekton.pipelines', 'gitops.fluxcd-argocd',
  'cloud.twelve-factor', 'cloud.k8s-serverless', 'cloud.progressive-delivery',
  'cloud.zero-trust-security', 'cloud.mtls-spiffe', 'cloud.api-container-security',
  'cloud.aws-core-services', 'cloud.aws-lambda-serverless', 'cloud.azure-core-services',
  'cloud.aws-vs-azure',
  'ansible.basics', 'terraform.basics', 'terraform.state-management',
  'terraform.modules-workspaces', 'terraform.cloud-enterprise', 'terraform.remote-state-backends',
  'observability.prometheus-basics', 'observability.nagios-alerting', 'observability.elk-basics',
  'observability.prometheus-advanced', 'observability.tracing', 'observability.alerting',
  'observability.scaling-infra',
  'aiops.fundamentals', 'aiops.cicd-automation', 'aiops.predictive-analytics',
  'aiops.incident-management', 'aiops.security', 'aiops.monitoring', 'aiops.self-healing',
];

async function main() {
  const db = new Kysely<Database>({
    dialect: new PostgresDialect({
      pool: new Pool({
        connectionString: process.env.DATABASE_URL ?? 'postgres://practice:practice@localhost:5433/practice_engine',
      }),
    }),
  });

  const domainSeq = new Map<string, number>();
  let updated = 0;
  let missing = 0;

  for (const slug of SLUG_ORDER) {
    const row = await db.selectFrom('skill.skill').select(['domain']).where('slug', '=', slug).executeTakeFirst();
    if (!row) {
      console.warn(`  ! not found in DB: ${slug}`);
      missing++;
      continue;
    }
    const sequence = (domainSeq.get(row.domain) ?? 0) + 1;
    domainSeq.set(row.domain, sequence);

    await db
      .updateTable('skill.skill')
      .set({ domain_order: DOMAIN_ORDER[row.domain] ?? 999, sequence })
      .where('slug', '=', slug)
      .execute();
    updated++;
  }

  console.log(`Updated ${updated} skills, ${missing} slugs not found in DB.`);
  await db.destroy();
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
