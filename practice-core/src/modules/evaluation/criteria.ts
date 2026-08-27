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
  /**
   * Count of validator runs the learner triggered before the final
   * submit-triggered one (trigger 'manual' or 'auto_debounce' -- see
   * ValidatorRunnerService). This is the evidence gate efficiency needs:
   * without it, a learner who submits seconds after starting with zero
   * commands run gets scored purely on elapsed time, which rewards
   * "fast" over "actually worked on it." Any prior validation run means
   * the learner interacted with the task at least once.
   */
  preSubmitValidationRunCount: number;
  /**
   * Doc §3.3 PRODUCTION_SIM process signal: count of commands the
   * learner ran that matched the activity's process_signals.blast_radius
   * .forbidden list (e.g. "kubectl delete ns"), accumulated by
   * CommandExecutedConsumer into attempt_signal as each COMMAND_EXECUTED
   * event arrives. 0 for GUIDED_LAB activities (no process_signals
   * authored, nothing to violate).
   */
  blastRadiusViolations: number;
  /**
   * Doc §3.3: counts of commands matching process_signals.
   * diagnostic_efficiency's good_actions/bad_actions lists, accumulated
   * by CommandExecutedConsumer into attempt_signal. Feeds
   * fn.diagnostic_efficiency.v1's ratio half.
   */
  diagnosticEfficiencyGoodCount: number;
  diagnosticEfficiencyBadCount: number;
  /**
   * Doc line 2047: "hypothesis_ordering — order of areas investigated
   * vs canonical path." commandSequence is every COMMAND_EXECUTED cmd
   * text for this attempt in seq order (from attempt_events, doc §4.2's
   * append-only log); canonicalDiagnosticPaths is one ordered list of
   * command-prefix strings per fault actually applied to this attempt
   * (FaultRepository reads content/faults/<id>.yaml's
   * canonical_diagnostic_path -- there's no fault/fault_version table,
   * see that repository's own doc comment). Empty for GUIDED_LAB
   * attempts (no faults applied, nothing to order against).
   */
  commandSequence: string[];
  canonicalDiagnosticPaths: string[][];
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
 * Below this, an attempt with no other evidence is treated as "no
 * genuine attempt" rather than scored on speed -- real typing, reading
 * task instructions, and running even one command takes longer than
 * this in practice, so an attempt this short with nothing else to show
 * for it is submit-immediately-without-attempting, not a fast solve.
 * Deliberately short (well under a realistic fastest-genuine-solve time)
 * since it only matters when neither of the two stronger signals below
 * fired -- it exists to catch the zero-effort case, not to gate normal
 * fast-but-real attempts.
 */
const MIN_ENGAGED_MINUTES_WITHOUT_OTHER_EVIDENCE = 0.17; // 10s

/**
 * fn.efficiency.v2: how close active time was to the authored estimate,
 * *gated on evidence the learner actually attempted the task* -- v1
 * scored pure elapsed time, which meant submitting seconds after
 * starting with no commands run scored as "100% efficient" (fast) rather
 * than "didn't attempt it." Evidence is any of:
 *   - at least one required task actually passed (the strongest
 *     signal -- passing validators cannot happen by sitting idle, so a
 *     learner who solved it fast and correctly must not be scored the
 *     same as one who submitted empty);
 *   - a pre-submit validation run (the learner explicitly checked their
 *     work at least once, even if the final result was wrong -- doc's
 *     "minor typo shouldn't zero out efficiency" case);
 *   - active time past MIN_ENGAGED_MINUTES_WITHOUT_OTHER_EVIDENCE, as a
 *     last-resort floor for the case neither of the above fired.
 * Below the time-ratio calculation itself is unchanged from v1: 1.0 at
 * or under estimate, decaying linearly to 0 at 3x estimate (doc §1.4
 * difficulty table's "3x median" generous L1 allowance).
 */
export const efficiencyV1: CriterionFn = (input) => {
  const anyTaskPassed = input.taskResults.some((t) => t.passed);
  const hasEvidence =
    anyTaskPassed ||
    input.preSubmitValidationRunCount > 0 ||
    input.activeMinutes >= MIN_ENGAGED_MINUTES_WITHOUT_OTHER_EVIDENCE;

  if (!hasEvidence) {
    return {
      value: 0,
      explanation: {
        reason:
          'no evidence of a genuine attempt (no task passed, no validation runs, submitted near-instantly)',
        active_minutes: input.activeMinutes,
        pre_submit_validation_run_count: input.preSubmitValidationRunCount,
      },
    };
  }

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
      any_task_passed: anyTaskPassed,
      pre_submit_validation_run_count: input.preSubmitValidationRunCount,
    },
  };
};

/**
 * fn.diagnostic_efficiency.v1: doc §3.3's "ratio" half of
 * "scoring: ratio_and_ordering" -- "did they read events/logs/describe
 * before acting?" as good_actions / (good_actions + bad_actions). No
 * evidence at all (a GUIDED_LAB attempt, or a PRODUCTION_SIM attempt
 * where the learner ran neither a good nor a bad action) returns 1.0:
 * absence of destructive/premature action is not evidence of poor
 * diagnosis, so this criterion should not drag an otherwise-fine
 * attempt down just because process_signals weren't authored or
 * triggered. The ordering half is a separate criterion
 * (hypothesisOrderingV1) since it needs the fault's canonical path, a
 * different input shape entirely.
 */
export const diagnosticEfficiencyV1: CriterionFn = (input) => {
  const good = input.diagnosticEfficiencyGoodCount;
  const bad = input.diagnosticEfficiencyBadCount;
  const total = good + bad;
  if (total === 0) {
    return {
      value: 1,
      explanation: {
        reason: 'no good/bad diagnostic actions observed',
        good,
        bad,
      },
    };
  }
  return {
    value: clamp01(good / total),
    explanation: { good_actions: good, bad_actions: bad, ratio: good / total },
  };
};

/**
 * fn.hypothesis_ordering.v1: doc line 2047's "order of areas
 * investigated vs canonical path." For each fault applied to this
 * attempt, walks its canonical_diagnostic_path (ordered command
 * prefixes) and finds the first learner command matching each step (a
 * command "matches" a step if it starts with that step's prefix, doing
 * the same substring-prefix comparison telemetry_tap-adjacent code uses
 * elsewhere in this codebase). Score is the fraction of canonical-step
 * pairs the learner investigated in the same relative order as the
 * canonical path (a normalised inversion count, the standard measure of
 * how "out of order" a sequence is) -- steps the learner never ran at
 * all are simply excluded from the pair-count rather than penalised
 * twice over (diagnostic_efficiency's ratio already covers "didn't
 * investigate enough"; this criterion is purely about order among what
 * *was* investigated).
 */
export const hypothesisOrderingV1: CriterionFn = (input) => {
  if (input.canonicalDiagnosticPaths.length === 0) {
    return {
      value: 1,
      explanation: {
        reason: 'no faults with a canonical diagnostic path applied',
      },
    };
  }

  const results = input.canonicalDiagnosticPaths.map((path) =>
    orderingScoreForPath(path, input.commandSequence),
  );
  const scored = results.filter((r) => r.comparablePairs > 0);
  if (scored.length === 0) {
    return {
      value: 1,
      explanation: {
        reason:
          'fewer than 2 canonical steps were ever investigated -- nothing to order',
      },
    };
  }

  const value = scored.reduce((sum, r) => sum + r.score, 0) / scored.length;
  return {
    value: clamp01(value),
    explanation: {
      per_fault: results.map((r) => ({
        score: r.score,
        comparable_pairs: r.comparablePairs,
      })),
    },
  };
};

function orderingScoreForPath(
  canonicalPath: string[],
  commandSequence: string[],
): { score: number; comparablePairs: number } {
  // First-occurrence index in the learner's real command sequence for
  // each canonical step, or -1 if the learner never ran a matching command.
  const firstSeenIndex = canonicalPath.map((step) =>
    commandSequence.findIndex((cmd) => cmd.startsWith(step)),
  );

  let correctlyOrderedPairs = 0;
  let comparablePairs = 0;
  for (let i = 0; i < firstSeenIndex.length; i++) {
    for (let j = i + 1; j < firstSeenIndex.length; j++) {
      if (firstSeenIndex[i] === -1 || firstSeenIndex[j] === -1) continue; // one/both never investigated -- not comparable
      comparablePairs++;
      if (firstSeenIndex[i] < firstSeenIndex[j]) correctlyOrderedPairs++; // canonical step i really did come before step j
    }
  }

  return {
    score: comparablePairs > 0 ? correctlyOrderedPairs / comparablePairs : 0,
    comparablePairs,
  };
}

/**
 * fn.troubleshooting.v1: doc §6.4's sp.production-sim.default weights
 * table names "troubleshooting" as its own top-level criterion (weight
 * .30), distinct from diagnostic_efficiency/hypothesis_ordering
 * themselves -- those two are the doc's process_signals (§3.3), the raw
 * material "troubleshooting" as a graded criterion is built from. Equal
 * blend of the two: efficiency (did they act on evidence, not guesses)
 * and ordering (did they investigate in a sensible sequence) are
 * different failure modes a learner can hit independently, so neither
 * should be able to fully compensate for the other.
 */
export const troubleshootingV1: CriterionFn = (input) => {
  const efficiency = diagnosticEfficiencyV1(input);
  const ordering = hypothesisOrderingV1(input);
  return {
    value: clamp01((efficiency.value + ordering.value) / 2),
    explanation: {
      diagnostic_efficiency: efficiency.value,
      hypothesis_ordering: ordering.value,
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
  efficiency: { version: 'fn.efficiency.v2', fn: efficiencyV1 },
  diagnostic_efficiency: {
    version: 'fn.diagnostic_efficiency.v1',
    fn: diagnosticEfficiencyV1,
  },
  hypothesis_ordering: {
    version: 'fn.hypothesis_ordering.v1',
    fn: hypothesisOrderingV1,
  },
  troubleshooting: {
    version: 'fn.troubleshooting.v1',
    fn: troubleshootingV1,
  },
  /**
   * doc §6.4's sp.production-sim.default names technical_implementation
   * and reliability as separate weighted criteria; neither has a doc-
   * specified computation distinct from what technical_correctness
   * already measures (weighted validator pass rate across every task's
   * validators -- a NO_REGRESSION validator authored into a task's
   * validators[] already flows into this, so "did the fix work without
   * breaking something else" is already covered when content authors
   * one). Registered as direct aliases rather than fabricating a
   * separate computation the doc never specifies -- revisit if/when a
   * dedicated signal for either is designed (e.g. a real security-
   * specific validator type, none of which are implemented yet).
   */
  technical_implementation: {
    version: 'fn.technical_correctness.v1',
    fn: technicalCorrectnessV1,
  },
  reliability: {
    version: 'fn.technical_correctness.v1',
    fn: technicalCorrectnessV1,
  },
};

function clamp01(n: number): number {
  return Math.min(1, Math.max(0, n));
}
