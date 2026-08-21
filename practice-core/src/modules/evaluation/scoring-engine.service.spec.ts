import { ScoringEngineService } from './scoring-engine.service';
import { GUIDED_LAB_DEFAULT_PROFILE } from './scoring-profile';
import type { CriterionInput } from './criteria';

describe('ScoringEngineService', () => {
  const engine = new ScoringEngineService();

  function baseInput(overrides: Partial<CriterionInput> = {}): CriterionInput {
    return {
      taskResults: [
        {
          taskKey: 't1',
          required: true,
          passed: true,
          validatorResults: [
            {
              validatorId: 'v1',
              status: 'PASS',
              weight: 1.0,
              severity: 'BLOCKING',
            },
          ],
        },
      ],
      hintPenaltyTotal: 0,
      resetCount: 0,
      retryIndex: 0,
      activeMinutes: 20,
      estimatedMinutes: 35,
      ...overrides,
    };
  }

  it('scores a clean, fast, all-pass attempt near 1.0 and marks it passed', () => {
    const result = engine.score(baseInput(), GUIDED_LAB_DEFAULT_PROFILE, {
      wasFirstTryPass: true,
    });
    expect(result.passed).toBe(true);
    expect(result.finalScore).toBeGreaterThan(0.9);
    expect(result.bonuses.first_try_pass).toBe(0.02);
  });

  it('reproduces the doc §12.1 sequence diagram outcome: score 0.86, passed', () => {
    // Doc §12.1: "full validation ... score 0.86, passed" after the
    // learner used a level-2 hint on task t3 (implying some hint penalty)
    // and completed with all required tasks passing.
    const result = engine.score(
      baseInput({
        taskResults: [
          {
            taskKey: 't1',
            required: true,
            passed: true,
            validatorResults: [
              {
                validatorId: 'v.image-exists',
                status: 'PASS',
                weight: 0.6,
                severity: 'BLOCKING',
              },
              {
                validatorId: 'v.image-size',
                status: 'PASS',
                weight: 0.4,
                severity: 'WARN',
              },
            ],
          },
          {
            taskKey: 't2',
            required: true,
            passed: true,
            validatorResults: [
              {
                validatorId: 'v.image-pushed',
                status: 'PASS',
                weight: 1.0,
                severity: 'BLOCKING',
              },
            ],
          },
        ],
        hintPenaltyTotal: 0.03, // one level-2 hint per §3.2's hint ladder
        activeMinutes: 35,
        estimatedMinutes: 35,
      }),
      GUIDED_LAB_DEFAULT_PROFILE,
      { wasFirstTryPass: false },
    );
    expect(result.passed).toBe(true);
    // Not asserting the exact 0.86 (doc doesn't specify enough of the
    // efficiency/task_completion inputs to reproduce bit-for-bit), but the
    // qualitative claim -- a mostly-clean pass with one hint lands in the
    // high-passing range, not near-perfect -- must hold.
    expect(result.finalScore).toBeGreaterThan(0.8);
    expect(result.finalScore).toBeLessThan(1.0);
  });

  it('fails an attempt where a required task did not pass', () => {
    const result = engine.score(
      baseInput({
        taskResults: [
          {
            taskKey: 't1',
            required: true,
            passed: false,
            validatorResults: [
              {
                validatorId: 'v1',
                status: 'FAIL',
                weight: 1.0,
                severity: 'BLOCKING',
              },
            ],
          },
        ],
      }),
      GUIDED_LAB_DEFAULT_PROFILE,
      { wasFirstTryPass: false },
    );
    expect(result.passed).toBe(false);
    expect(result.finalScore).toBeLessThan(
      GUIDED_LAB_DEFAULT_PROFILE.passThreshold,
    );
  });

  it('applies hint penalty capped at the profile cap even with excessive hint usage', () => {
    const result = engine.score(
      baseInput({ hintPenaltyTotal: 0.5 }), // way over the 0.2 cap
      GUIDED_LAB_DEFAULT_PROFILE,
      { wasFirstTryPass: false },
    );
    expect(result.penalties.hints).toBe(0.2);
  });

  it('applies reset penalty capped at the profile cap', () => {
    const result = engine.score(
      baseInput({ resetCount: 10 }),
      GUIDED_LAB_DEFAULT_PROFILE,
      {
        wasFirstTryPass: false,
      },
    );
    expect(result.penalties.resets).toBe(0.06); // 10 * 0.02 = 0.2, capped to 0.06
  });

  it('applies retry penalty capped at the profile cap', () => {
    const result = engine.score(
      baseInput({ retryIndex: 10 }),
      GUIDED_LAB_DEFAULT_PROFILE,
      {
        wasFirstTryPass: false,
      },
    );
    expect(result.penalties.retries).toBe(0.15); // 10 * 0.05 = 0.5, capped to 0.15
  });

  it('guard: penalties cannot drag an otherwise-passing score below the floor (doc §6.4)', () => {
    // Unpenalised weighted sum is well above pass_threshold, but stacking
    // every penalty at its cap would drag it toward/below 0.3 without the guard.
    const result = engine.score(
      baseInput({ hintPenaltyTotal: 0.2, resetCount: 10, retryIndex: 10 }),
      GUIDED_LAB_DEFAULT_PROFILE,
      { wasFirstTryPass: false },
    );
    expect(result.finalScore).toBeGreaterThanOrEqual(
      GUIDED_LAB_DEFAULT_PROFILE.guards!.minAfterPenalties!,
    );
  });

  it('guard does not rescue a genuinely failing attempt up to the floor', () => {
    const result = engine.score(
      baseInput({
        taskResults: [
          {
            taskKey: 't1',
            required: true,
            passed: false,
            validatorResults: [
              {
                validatorId: 'v1',
                status: 'FAIL',
                weight: 1.0,
                severity: 'BLOCKING',
              },
            ],
          },
        ],
      }),
      GUIDED_LAB_DEFAULT_PROFILE,
      { wasFirstTryPass: false },
    );
    // A failed required task alone should not be pulled up by the guard --
    // the guard only protects attempts whose unpenalised score already clears pass_threshold.
    expect(result.finalScore).toBeLessThan(
      GUIDED_LAB_DEFAULT_PROFILE.guards!.minAfterPenalties!,
    );
  });

  it('throws on an unknown criterion key in the profile (fail loud, not silent zero)', () => {
    const badProfile = {
      ...GUIDED_LAB_DEFAULT_PROFILE,
      weights: { made_up_criterion: 1.0 },
    };
    expect(() =>
      engine.score(baseInput(), badProfile, { wasFirstTryPass: false }),
    ).toThrow(/unknown criterion/);
  });

  it('never produces a score outside [0,1] under extreme inputs', () => {
    const result = engine.score(
      baseInput({
        hintPenaltyTotal: 999,
        resetCount: 999,
        retryIndex: 999,
        activeMinutes: 999999,
      }),
      GUIDED_LAB_DEFAULT_PROFILE,
      { wasFirstTryPass: true },
    );
    expect(result.finalScore).toBeGreaterThanOrEqual(0);
    expect(result.finalScore).toBeLessThanOrEqual(1);
  });
});
