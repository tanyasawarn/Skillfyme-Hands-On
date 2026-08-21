/**
 * Lints every YAML under content/faults/ against contracts/fault.schema.json
 * (JSON Schema structural check) plus one relational check the schema can't
 * express: every skills_probed slug must be a real, active skill. Mirrors
 * lint-content.ts's pattern for activities -- same CI discipline (doc
 * §3.5 step 1: "skills exist") applied to the fault primitive.
 *
 * Run with: npx ts-node -r tsconfig-paths/register scripts/lint-faults.ts
 */
import * as fs from 'node:fs';
import * as path from 'node:path';
import * as yaml from 'js-yaml';
import Ajv2020 from 'ajv/dist/2020';
import addFormats from 'ajv-formats';
import { Kysely, PostgresDialect } from 'kysely';
import { Pool } from 'pg';
import type { Database } from '../src/db/schema';

async function main() {
  const db = new Kysely<Database>({
    dialect: new PostgresDialect({
      pool: new Pool({
        connectionString: process.env.DATABASE_URL ?? 'postgres://practice:practice@localhost:5433/practice_engine',
      }),
    }),
  });

  const ajv = new Ajv2020({ allErrors: true, strict: false });
  addFormats(ajv);
  const schemaPath = path.resolve(__dirname, '../../contracts/fault.schema.json');
  const schema = JSON.parse(fs.readFileSync(schemaPath, 'utf-8'));
  const validate = ajv.compile(schema);

  const knownSkills = await db.selectFrom('skill.skill').select('slug').execute();
  const knownSet = new Set(knownSkills.map((s) => s.slug));

  const dir = path.resolve(__dirname, '../../content/faults');
  const files = fs.existsSync(dir) ? fs.readdirSync(dir).filter((f) => f.endsWith('.yaml')) : [];

  let ok = 0;
  let bad = 0;
  const seenIds = new Set<string>();

  for (const file of files) {
    const source = fs.readFileSync(path.join(dir, file), 'utf-8');
    const spec = yaml.load(source) as { id?: string; skills_probed?: string[] };

    const valid = validate(spec);
    const issues: string[] = [];

    if (!valid) {
      for (const err of validate.errors ?? []) {
        issues.push(`${err.instancePath || '(root)'}: ${err.message ?? 'schema violation'}`);
      }
    } else {
      if (spec.id && seenIds.has(spec.id)) {
        issues.push(`duplicate fault id: ${spec.id}`);
      }
      if (spec.id) seenIds.add(spec.id);

      for (const slug of spec.skills_probed ?? []) {
        if (!knownSet.has(slug)) {
          issues.push(`skills_probed references unknown skill slug: ${slug}`);
        }
      }
    }

    if (issues.length === 0) {
      ok++;
    } else {
      bad++;
      console.error(`${file}: FAIL`);
      for (const issue of issues) console.error(`    ${issue}`);
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
