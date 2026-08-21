/**
 * Lints every YAML under content/activities/ via the real SpecLintService
 * pipeline (JSON Schema + skill-existence + prerequisite-DAG-slug checks),
 * without publishing anything. Run repeatedly while authoring lab content
 * so errors surface immediately instead of at publish time.
 *
 * Run with: npx ts-node -r tsconfig-paths/register scripts/lint-content.ts
 */
import * as fs from 'node:fs';
import * as path from 'node:path';
import { Kysely, PostgresDialect } from 'kysely';
import { Pool } from 'pg';
import type { Database } from '../src/db/schema';
import { SpecLintService } from '../src/modules/content-ci/spec-lint.service';

async function main() {
  const db = new Kysely<Database>({
    dialect: new PostgresDialect({
      pool: new Pool({
        connectionString: process.env.DATABASE_URL ?? 'postgres://practice:practice@localhost:5433/practice_engine',
      }),
    }),
  });

  const lint = new SpecLintService();
  const knownSkills = await db.selectFrom('skill.skill').select('slug').execute();
  const knownSet = new Set(knownSkills.map((s) => s.slug));

  const dir = path.resolve(__dirname, '../../content/activities');
  const files = fs.readdirSync(dir).filter((f) => f.endsWith('.yaml'));

  let ok = 0;
  let bad = 0;
  for (const file of files) {
    const source = fs.readFileSync(path.join(dir, file), 'utf-8');
    let spec: unknown;
    try {
      spec = lint.parseYaml(source);
    } catch (err) {
      console.error(`${file}: YAML PARSE ERROR: ${err instanceof Error ? err.message : err}`);
      bad++;
      continue;
    }
    const result = lint.lint(spec, knownSet);
    if (result.valid) {
      ok++;
    } else {
      bad++;
      console.error(`${file}: FAIL`);
      for (const issue of result.issues) {
        console.error(`    ${issue.path}: ${issue.message}`);
      }
    }
  }

  console.log(`\n${ok} passed, ${bad} failed (of ${files.length} files).`);
  await db.destroy();
  process.exit(bad > 0 ? 1 : 0);
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
