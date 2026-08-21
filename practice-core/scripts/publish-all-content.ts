/**
 * Lints and publishes every YAML under content/activities/ through the
 * real pipeline (SpecLintService -> CatalogRepository), same as
 * publish-demo-activity.ts but for the whole directory instead of one
 * hardcoded file. Safe to re-run: publishNewVersion throws a clear
 * ConflictException (doc §3.6, version already published) for anything
 * already published at its current spec version -- this script reports
 * those as "already published" and continues rather than aborting the
 * batch.
 *
 * Run with: npx ts-node -r tsconfig-paths/register scripts/publish-all-content.ts
 */
import * as fs from 'node:fs';
import * as path from 'node:path';
import { Kysely, PostgresDialect } from 'kysely';
import { Pool } from 'pg';
import type { Database } from '../src/db/schema';
import { SpecLintService } from '../src/modules/content-ci/spec-lint.service';
import { CatalogRepository } from '../src/modules/catalog/catalog.repository';

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
  const knownSkills = await db.selectFrom('skill.skill').select('slug').execute();
  const knownSet = new Set(knownSkills.map((s) => s.slug));

  const dir = path.resolve(__dirname, '../../content/activities');
  const files = fs.readdirSync(dir).filter((f) => f.endsWith('.yaml')).sort();

  let published = 0;
  let alreadyPublished = 0;
  let failed = 0;

  for (const file of files) {
    const source = fs.readFileSync(path.join(dir, file), 'utf-8');
    const spec = lint.parseYaml(source) as {
      id: string;
      version: number;
      mode: 'GUIDED_LAB' | 'PRODUCTION_SIM' | 'PROJECT';
      meta: { difficulty_level: string; estimated_minutes: number };
      environment: { blueprint: string; cost_budget_usd: number };
      skills: Array<{ skill: string; weight: number; primary: boolean; bloom?: string }>;
    };

    const result = lint.lint(spec, knownSet);
    if (!result.valid) {
      console.error(`${file}: LINT FAILED`);
      for (const issue of result.issues) console.error(`    ${issue.path}: ${issue.message}`);
      failed++;
      continue;
    }

    try {
      const version = await catalog.publishNewVersion({
        tenantId: DEMO_TENANT_ID,
        activitySlug: spec.id,
        mode: spec.mode,
        spec,
      });
      console.log(`${file}: published v${version.version} (${version.id})`);
      published++;
    } catch (err) {
      if (err instanceof Error && err.message.includes('already')) {
        console.log(`${file}: already published, skipping`);
        alreadyPublished++;
      } else if (err instanceof Error && err.message.includes('re-sync')) {
        console.log(`${file}: already published at this version, skipping`);
        alreadyPublished++;
      } else {
        console.error(`${file}: PUBLISH FAILED: ${err instanceof Error ? err.message : err}`);
        failed++;
      }
    }
  }

  console.log(`\n${published} published, ${alreadyPublished} already published, ${failed} failed (of ${files.length} files).`);
  await db.destroy();
  process.exit(failed > 0 ? 1 : 0);
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
