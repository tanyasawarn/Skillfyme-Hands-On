/**
 * Doc §6.4: "Criterion computation is code, not config -- each criterion
 * is a named versioned function (fn.technical_correctness.v2) that reads
 * signals and returns [0,1] plus an explanation object." Phase 1 scope:
 * sp.guided-lab.default's three criteria (technical_correctness,
 * task_completion, efficiency) -- the other profiles' criteria
 * (troubleshooting, architecture, etc.) are Phase 2/3 territory tied to
 * signals (diagnostic_efficiency, blast_radius) that don't exist until
 * Production Sims and Projects are built.
 */

export interface CriterionInput {
  /** Per-task pass/fail from ValidatorRunnerService, weighted by validator weight within the task. */
  taskResults: Array<{
    taskKey: string;
    required: boolean;
    passed: boolean;
    validatorResults: Array<{
      validatorId: string;
      status: string;
      weight: number;
      severity: 'BLOCKING' | 'WARN';
    }>;
  }>;
  /** doc §6.4 penalty signals. */
  hintPenaltyTotal: number;
  resetCount: number;
  retryIndex: number;
  /** Wall-clock minutes spent vs. the activity's estimated_minutes. */
  activeMinutes: number;
  estimatedMinutes: number;
}

export interface CriterionResult {
  value: number; // [0,1]
  explanation: Record<string, unknown>;
}

export type CriterionFn = (input: CriterionInput) => CriterionResult;

/**
 * fn.technical_correctness.v1: weighted pass rate across all validators
 * (BLOCKING and WARN both contribute -- WARN validators are "score good
 * practice... without blocking progress" per §3.2 design notes, so they
 * affect this criterion's value even though they don't gate task
 * completion).
 */
export const technicalCorrectnessV1: CriterionFn = (input) => {
  const allValidators = input.taskResults.flatMap((t) => t.validatorResults);
  const scorable = allValidators.filter((v) => v.status !== 'ERROR');
  if (scorable.length === 0)
    return { value: 0, explanation: { reason: 'no scorable validators' } };

  const totalWeight = scorable.reduce((sum, v) => sum + v.weight, 0);
  const passedWeight = scorable
    .filter((v) => v.status === 'PASS')
    .reduce((sum, v) => sum + v.weight, 0);
  const value = totalWeight > 0 ? passedWeight / totalWeight : 0;

  return {
    value: clamp01(value),
    explanation: {
      passed_weight: passedWeight,
      total_weight: totalWeight,
      validator_count: scorable.length,
    },
  };
};

/** fn.task_completion.v1: fraction of *required* tasks that passed. */
export const taskCompletionV1: CriterionFn = (input) => {
  const required = input.taskResults.filter((t) => t.required);
  if (required.length === 0)
    return { value: 1, explanation: { reason: 'no required tasks' } };
  const passedCount = required.filter((t) => t.passed).length;
  return {
    value: clamp01(passedCount / required.length),
    explanation: { passed: passedCount, required: required.length },
  };
};

/**
 * fn.efficiency.v1: how close active time was to the authored estimate.
 * 1.0 at or under estimate, decaying linearly to 0 at 3x estimate (doc
 * §1.4 difficulty table uses "3x median" as the generous L1 time
 * allowance, so 3x is used here as the point efficiency bottoms out
 * rather than an arbitrary cutoff).
 */
export const efficiencyV1: CriterionFn = (input) => {
  if (input.estimatedMinutes <= 0)
    return {
      value: 1,
      explanation: { reason: 'no estimate to compare against' },
    };
  const ratio = input.activeMinutes / input.estimatedMinutes;
  const value = ratio <= 1 ? 1 : clamp01(1 - (ratio - 1) / 2); // reaches 0 at ratio=3
  return {
    value,
    explanation: {
      active_minutes: input.activeMinutes,
      estimated_minutes: input.estimatedMinutes,
      ratio,
    },
  };
};

export const CRITERION_REGISTRY: Record<
  string,
  { version: string; fn: CriterionFn }
> = {
  technical_correctness: {
    version: 'fn.technical_correctness.v1',
    fn: technicalCorrectnessV1,
  },
  task_completion: { version: 'fn.task_completion.v1', fn: taskCompletionV1 },
  efficiency: { version: 'fn.efficiency.v1', fn: efficiencyV1 },
};

function clamp01(n: number): number {
  return Math.min(1, Math.max(0, n));
}
