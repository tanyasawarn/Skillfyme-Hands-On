/**
 * One-off dev script: lints and publishes content/activities/lab.k8s.deploy-node-app.yaml
 * through the real pipeline (SpecLintService -> CatalogRepository), so
 * local frontend dev has real, validated content to browse rather than
 * hand-inserted SQL. Not part of the app; run with:
 *   npx ts-node -r tsconfig-paths/register scripts/publish-demo-activity.ts
 */
import * as fs from 'node:fs';
import * as path from 'node:path';
import { Kysely, PostgresDialect } from 'kysely';
import { Pool } from 'pg';
import type { Database } from '../src/db/schema';
import { SpecLintService } from '../src/modules/content-ci/spec-lint.service';
import { CatalogRepository } from '../src/modules/catalog/catalog.repository';
import type { ActivitySpec } from '../src/modules/catalog/activity-spec';

const DEMO_TENANT_ID = '11111111-1111-1111-1111-111111111111';

async function main() {
  const db = new Kysely<Database>({
    dialect: new PostgresDialect({
      pool: new Pool({
        connectionString: process.env.DATABASE_URL ?? 'postgres://practice:practice@localhost:5433/practice_engine',
      }),
    }),
  });

  const lint = new SpecLintService();
  const catalog = new CatalogRepository(db);

  const yamlPath = path.resolve(__dirname, '../../content/activities/lab.k8s.deploy-node-app.yaml');
  const source = fs.readFileSync(yamlPath, 'utf-8');
  const spec = lint.parseYaml(source) as ActivitySpec;

  const knownSkills = await db.selectFrom('skill.skill').select('slug').execute();
  const result = lint.lint(spec, new Set(knownSkills.map((s) => s.slug)));
  if (!result.valid) {
    console.error('LINT FAILED:', JSON.stringify(result.issues, null, 2));
    process.exit(1);
  }
  console.log('Lint passed.');

  const version = await catalog.publishNewVersion({
    tenantId: DEMO_TENANT_ID,
    activitySlug: spec.id,
    mode: spec.mode,
    spec,
  });
  console.log(`Published ${spec.id} v${version.version} (activity_version_id=${version.id})`);

  await db.destroy();
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
