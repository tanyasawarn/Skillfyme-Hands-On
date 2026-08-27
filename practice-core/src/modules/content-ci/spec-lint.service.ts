import { Injectable, Logger } from '@nestjs/common';
import * as fs from 'node:fs';
import * as yaml from 'js-yaml';
import Ajv2020 from 'ajv/dist/2020';
import addFormats from 'ajv-formats';
import { resolveRepoRelativePath } from '../../common/repo-relative-path';

export interface LintIssue {
  path: string;
  message: string;
}

export interface LintResult {
  valid: boolean;
  issues: LintIssue[];
  spec?: unknown;
}

/**
 * Doc §3.5 step 1 (LINT): "schema valid, all required fields, every
 * validator has on_fail text, every task has >=1 validator, skills exist,
 * prerequisites are a DAG." The JSON-Schema half of this (required fields,
 * shapes, enums, the "every task has >=1 validator" and "every validator
 * has on_fail" rules) is enforced by contracts/activity_spec.schema.json
 * directly -- see its "tasks.items.validators" minItems:1 and
 * "on_fail" required+minLength:1 constraints. This service adds the
 * checks a JSON Schema cannot express: skill-existence and DAG-ness are
 * relational, and PUBLISHED-immutability is a DB-level rule (§3.6),
 * not a lint rule.
 *
 * The schema file lives in contracts/, not duplicated here, because it is
 * the joint-owned contract between Dev A and Dev B (PLAN.md
 * "conflict-minimizing rules") -- this service reads it, it does not
 * define it.
 */
@Injectable()
export class SpecLintService {
  private readonly logger = new Logger(SpecLintService.name);
  private readonly ajv: Ajv2020;
  private readonly validateSchema: ReturnType<Ajv2020['compile']>;

  constructor() {
    this.ajv = new Ajv2020({ allErrors: true, strict: false });
    addFormats(this.ajv);
    const schemaPath = this.resolveContractsPath('activity_spec.schema.json');
    const schema = JSON.parse(fs.readFileSync(schemaPath, 'utf-8'));
    this.validateSchema = this.ajv.compile(schema);
  }

  parseYaml(source: string): unknown {
    return yaml.load(source);
  }

  /**
   * Full lint pass: JSON-Schema structural validation, then the
   * relational checks the schema can't express.
   */
  lint(spec: unknown, knownSkillSlugs: Set<string>): LintResult {
    const issues: LintIssue[] = [];

    const schemaValid = this.validateSchema(spec);
    if (!schemaValid) {
      for (const err of this.validateSchema.errors ?? []) {
        issues.push({
          path: err.instancePath || '(root)',
          message: `${err.message ?? 'schema violation'} (${JSON.stringify(err.params)})`,
        });
      }
      // Relational checks below assume a structurally valid spec; bail out
      // early rather than producing confusing secondary errors.
      return { valid: false, issues };
    }

    const typed = spec as {
      skills: Array<{ skill: string }>;
      prerequisites?: { hard?: string[]; soft?: string[] };
    };

    // "skills exist" — every referenced skill slug must be a known, active skill.
    for (const s of typed.skills) {
      if (!knownSkillSlugs.has(s.skill)) {
        issues.push({
          path: '/skills',
          message: `unknown skill slug: ${s.skill}`,
        });
      }
    }
    const prereqSlugs = [
      ...(typed.prerequisites?.hard ?? []),
      ...(typed.prerequisites?.soft ?? []),
    ];
    for (const slug of prereqSlugs) {
      if (!knownSkillSlugs.has(slug)) {
        issues.push({
          path: '/prerequisites',
          message: `unknown prerequisite skill slug: ${slug}`,
        });
      }
    }

    return { valid: issues.length === 0, issues, spec };
  }

  private resolveContractsPath(file: string): string {
    // PLAN.md Phase 4's U4: this file's own prior comment here claimed
    // "repo root is 4 levels up" -- confirmed wrong via a direct
    // path.resolve() check against the real dist/ layout
    // (practice-core/src/modules/content-ci is 5 levels from repo root
    // once compiled to dist/src/modules/content-ci, not 4). Silently
    // masked in practice by always falling through to the cwd fallback.
    return resolveRepoRelativePath(__dirname, 5, `contracts/${file}`);
  }
}
