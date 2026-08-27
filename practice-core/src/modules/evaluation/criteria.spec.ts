import { efficiencyV1, type CriterionInput } from './criteria';

describe('efficiencyV1 (fn.efficiency.v2)', () => {
  function baseInput(overrides: Partial<CriterionInput> = {}): CriterionInput {
    return {
      taskResults: [],
      hintPenaltyTotal: 0,
      resetCount: 0,
      retryIndex: 0,
      activeMinutes: 20,
      estimatedMinutes: 35,
      preSubmitValidationRunCount: 0,
      blastRadiusViolations: 0,
      diagnosticEfficiencyGoodCount: 0,
      diagnosticEfficiencyBadCount: 0,
      commandSequence: [],
      canonicalDiagnosticPaths: [],
      ...overrides,
    };
  }

  it('scores 0 for an instant submission with no validation runs (no evidence of an attempt)', () => {
    const result = efficiencyV1(
      baseInput({ activeMinutes: 4 / 60, preSubmitValidationRunCount: 0 }),
    );
    expect(result.value).toBe(0);
    expect(result.explanation.reason).toMatch(/no evidence/);
  });

  it('does not award 100% just for submitting fast with zero engagement', () => {
    const result = efficiencyV1(
      baseInput({ activeMinutes: 0.01, preSubmitValidationRunCount: 0 }),
    );
    expect(result.value).toBe(0);
  });

  it('scores normally (time-ratio based) once there is at least one pre-submit validation run, even if fast', () => {
    const result = efficiencyV1(
      baseInput({
        activeMinutes: 5,
        estimatedMinutes: 20,
        preSubmitValidationRunCount: 1,
      }),
    );
    expect(result.value).toBe(1); // under estimate, evidence present -> full credit
  });

  it('scores normally once active time alone clears the engagement floor, even with zero validation runs', () => {
    const result = efficiencyV1(
      baseInput({
        activeMinutes: 15,
        estimatedMinutes: 20,
        preSubmitValidationRunCount: 0,
      }),
    );
    expect(result.value).toBe(1);
  });

  // Regression: a real attempt scored 0% efficiency because it passed
  // both required tasks in 29.9s with zero pre-submit validation runs --
  // fast + correct got treated identically to fast + empty. A learner
  // cannot pass a required task's validators by sitting idle, so a
  // passed task is evidence on its own, independent of time or
  // validation-run count.
  it('scores normally when a required task passed, even with a short time and zero validation runs', () => {
    const result = efficiencyV1(
      baseInput({
        activeMinutes: 30 / 60, // 30s, well under the old (removed) 30s floor
        estimatedMinutes: 20,
        preSubmitValidationRunCount: 0,
        taskResults: [
          { taskKey: 't1', required: true, passed: true, validatorResults: [] },
        ],
      }),
    );
    expect(result.value).toBe(1);
    expect(result.explanation.any_task_passed).toBe(true);
  });

  it('still scores 0 for an instant empty submission even though the evidence check now also looks at taskResults', () => {
    const result = efficiencyV1(
      baseInput({
        activeMinutes: 4 / 60,
        preSubmitValidationRunCount: 0,
        taskResults: [
          {
            taskKey: 't1',
            required: true,
            passed: false,
            validatorResults: [],
          },
        ],
      }),
    );
    expect(result.value).toBe(0);
  });

  it('a genuine attempt with a minor typo (task fails) still gets efficiency credit, separate from correctness', () => {
    // Learner ran the right commands (evidenced by a pre-submit validation
    // run) but the final result is wrong -- efficiency and correctness are
    // separate criteria, so this criterion alone should not be zeroed out
    // just because the task ultimately failed. Correctness/completion are
    // computed by their own criterion functions from taskResults.
    const result = efficiencyV1(
      baseInput({
        activeMinutes: 8,
        estimatedMinutes: 20,
        preSubmitValidationRunCount: 3,
      }),
    );
    expect(result.value).toBe(1);
  });

  it('decays with excessive time even when evidence of a genuine attempt exists', () => {
    const result = efficiencyV1(
      baseInput({
        activeMinutes: 60,
        estimatedMinutes: 20,
        preSubmitValidationRunCount: 2,
      }),
    );
    // ratio = 3 -> bottoms out at 0, but this is "took too long," not "didn't attempt."
    expect(result.value).toBe(0);
    expect(result.explanation.reason).toBeUndefined();
    expect(result.explanation.ratio).toBe(3);
  });
});
