import { Injectable } from '@nestjs/common';
import * as fs from 'node:fs';
import * as path from 'node:path';
import * as yaml from 'js-yaml';
import { resolveRepoRelativePath } from '../../common/repo-relative-path';

export interface RubricLevel {
  level: number;
  descriptor: string;
}

export interface RubricExemplar {
  level: number;
  text: string;
}

export interface RubricCriterion {
  key: string;
  title: string;
  description: string;
  levels: RubricLevel[];
  exemplars?: RubricExemplar[];
}

export interface Rubric {
  id: string;
  version: number;
  artifactType: string;
  requiredSections: string[];
  criteria: RubricCriterion[];
  humanReviewPolicy: string;
}

/**
 * Doc §6.5 rule 31: rubrics are versioned content with levels/descriptors/
 * exemplars stored alongside them, same content-as-code pattern as
 * FaultRepository reads content/faults/*.yaml -- there is no rubric/
 * rubric_version table any more than there's a fault one (see
 * FaultRepository's own doc comment on why: never built, PLAN.md Phase 2
 * scope). Read-only, no caching -- rubric count is tiny and grading is
 * not a hot path.
 */
@Injectable()
export class RubricRepository {
  private readonly rubricsDir = this.resolveRubricsDir();

  private resolveRubricsDir(): string {
    // PLAN.md Phase 4's U4: was 4 levels up, confirmed wrong (this file
    // is 5 levels from repo root once compiled to dist/src/modules/
    // evaluation) -- silently masked by always falling through to the
    // cwd fallback.
    return resolveRepoRelativePath(__dirname, 5, 'content/rubrics');
  }

  getRubric(rubricId: string): Rubric | null {
    const filePath = path.join(this.rubricsDir, `${rubricId}.yaml`);
    if (!fs.existsSync(filePath)) return null;

    try {
      const doc = yaml.load(fs.readFileSync(filePath, 'utf-8')) as {
        id: string;
        version: number;
        artifact_type: string;
        required_sections: string[];
        criteria: Array<{
          key: string;
          title: string;
          description: string;
          levels: RubricLevel[];
          exemplars?: RubricExemplar[];
        }>;
        human_review?: { policy?: string };
      };
      return {
        id: doc.id,
        version: doc.version,
        artifactType: doc.artifact_type,
        requiredSections: doc.required_sections ?? [],
        criteria: doc.criteria ?? [],
        humanReviewPolicy:
          doc.human_review?.policy ?? 'ALWAYS_PROVISIONAL_UNTIL_CALIBRATED',
      };
    } catch {
      return null;
    }
  }
}
