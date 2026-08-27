import { Injectable, Logger } from '@nestjs/common';
import type {
  AiGrader,
  GradingFacts,
  GradingResult,
  CriterionGrade,
} from './ai-grader.interface';
import type { Rubric } from './rubric.repository';

/**
 * Doc §7's AI Gateway / §6.5's AI-graded rubric path has no real LLM
 * provider configured anywhere in this project (confirmed: no
 * ANTHROPIC_API_KEY/OPENAI_API_KEY in any .env file repo-wide, ai-gateway/
 * is an empty directory). Same PLAN.md Phase 0 pattern as
 * FakeOrchestratorClient/FakeValidatorExecutor: this stands in behind the
 * real interface (AiGrader) so the artifact-submission pipeline, rubric
 * lookup, and scoring integration are all real and exercised end-to-end
 * -- only the actual model call is stubbed. Swapping in a real grader
 * (Claude via the Messages API, doc rule 32's structured-output contract
 * maps directly onto tool use / a JSON schema response) is a single
 * provider binding change in evaluation.module.ts, same "swap the mock"
 * shape as every other Phase-0-stubbed boundary in this codebase.
 *
 * Deterministic, not random: every criterion always grades at the
 * midpoint of its level scale with confidence 0 and a flag naming this
 * as a stub result. A random or fixed-high grade would be actively
 * misleading (a learner could read a fabricated "level 4, well
 * justified" and believe an LLM actually assessed their incident note);
 * confidence 0 + an explicit flag is the honest signal that no real
 * grading happened, which the caller (EvaluationService) must route to
 * 100% human review rather than trusting -- exactly rub.incident-note.v2
 * .yaml's own human_review.policy: ALWAYS_PROVISIONAL_UNTIL_CALIBRATED,
 * which this stub result is a degenerate case of.
 */
@Injectable()
export class FakeAiGrader implements AiGrader {
  private readonly logger = new Logger(FakeAiGrader.name);

  async grade(rubric: Rubric, facts: GradingFacts): Promise<GradingResult> {
    this.logger.debug(
      `[fake] grading rubric=${rubric.id} artifact_length=${facts.artifactText.length}`,
    );

    const criterionGrades: CriterionGrade[] = rubric.criteria.map(
      (criterion) => {
        const levels = criterion.levels.map((l) => l.level);
        const midpointLevel =
          levels[Math.floor((levels.length - 1) / 2)] ?? levels[0] ?? 1;
        return {
          criterion: criterion.key,
          level: midpointLevel,
          confidence: 0,
          evidenceQuotes: [],
          justification:
            'FakeAiGrader stub: no real model call was made. This grade is not meaningful and must be human-reviewed before use.',
          flags: ['STUB_GRADER_NO_REAL_INFERENCE'],
        };
      },
    );

    return {
      rubricId: rubric.id,
      criterionGrades,
      provisional: true,
      provisionalReason:
        'FakeAiGrader stub -- no real LLM provider configured (see class doc comment)',
    };
  }
}
