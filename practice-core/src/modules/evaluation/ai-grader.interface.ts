import type { Rubric } from './rubric.repository';

/**
 * Doc §6.5 rule 34: "Deterministic pre-processing. Feed the grader
 * facts, not raw environments." Doc §1.3.2 step 9: "the deterministic
 * facts (which fault was actually present, what the learner actually
 * did) supplied to the grader, so it can judge whether the stated root
 * cause is CORRECT, not just well-written." This is what makes the
 * grading request "engineered rather than prompted casually" (§6.5's own
 * framing) -- the grader never sees a live environment or raw learner
 * telemetry, only these pre-established facts plus the learner's
 * artifact text.
 */
export interface GradingFacts {
  /** The learner's submitted artifact text (e.g. the incident note markdown). */
  artifactText: string;
  /** Ground truth: fault ids actually applied to this attempt (doc's "which fault was actually present"). */
  appliedFaultIds: string[];
  /** Ground truth: did the learner's fix pass the resolution validators (NO_REGRESSION, K8S_ASSERT, HTTP_SLO, ...). */
  resolutionValidatorResults: Array<{ validatorId: string; status: string }>;
  /** Ground truth: real command sequence the learner ran (doc's "what the learner actually did"). */
  commandSequence: string[];
}

/**
 * Doc §6.5 rule 32: "Structured output only. JSON: {criterion, level,
 * confidence, evidence_quotes[], justification, flags[]}. Schema-
 * validated; malformed output is retried then escalated." One of these
 * per rubric criterion.
 */
export interface CriterionGrade {
  criterion: string;
  level: number;
  confidence: number; // [0,1]
  evidenceQuotes: string[];
  justification: string;
  flags: string[];
}

export interface GradingResult {
  rubricId: string;
  criterionGrades: CriterionGrade[];
  /**
   * Doc rule 33: "Multi-sample with agreement check... If still
   * divergent -> route to human review and mark the score provisional."
   * True whenever this grading is not yet a final, trusted score --
   * either because agreement failed, or (Phase 2's actual state, see
   * FakeAiGrader/rub.incident-note.v2.yaml's human_review.policy) no
   * calibration harness has run against this rubric yet, so doc rule
   * 33's own fallback applies: every score is provisional until
   * calibrated, not just the disagreement case.
   */
  provisional: boolean;
  provisionalReason?: string;
}

export const AI_GRADER = Symbol('AI_GRADER');

export interface AiGrader {
  /**
   * Grades one artifact against one rubric, given the deterministic
   * facts a real prompt would be built from. Doc rule 35's prompt-
   * injection defence ("learner content is enclosed in delimited,
   * clearly-labelled untrusted blocks") is the real implementation's
   * responsibility, not this interface's -- it's a prompt-construction
   * detail internal to whichever AiGrader actually calls out to a model.
   */
  grade(rubric: Rubric, facts: GradingFacts): Promise<GradingResult>;
}
