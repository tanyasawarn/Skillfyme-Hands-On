import { ScoringEngineService } from './scoring-engine.service';
import {
  GUIDED_LAB_DEFAULT_PROFILE,
  PRODUCTION_SIM_DEFAULT_PROFILE,
} from './scoring-profile';
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
      preSubmitValidationRunCount: 1,
      blastRadiusViolations: 0,
      diagnosticEfficiencyGoodCount: 0,
      diagnosticEfficiencyBadCount: 0,
      commandSequence: [],
      canonicalDiagnosticPaths: [],
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

  // Doc §3.3: penalties.blast_radius: {per_violation: 0.15, cap: 0.30} --
  // GUIDED_LAB_DEFAULT_PROFILE has no blastRadius penalty configured (it's
  // a PRODUCTION_SIM-only concern), so these tests build a profile that
  // mirrors the doc's worked example directly.
  const PROFILE_WITH_BLAST_RADIUS = {
    ...GUIDED_LAB_DEFAULT_PROFILE,
    penalties: {
      ...GUIDED_LAB_DEFAULT_PROFILE.penalties,
      blastRadius: { perViolation: 0.15, cap: 0.3 },
    },
  };

  it('applies no blast_radius penalty when the profile does not configure one', () => {
    const result = engine.score(
      baseInput({ blastRadiusViolations: 5 }),
      GUIDED_LAB_DEFAULT_PROFILE,
      { wasFirstTryPass: false },
    );
    expect(result.penalties.blast_radius).toBeUndefined();
  });

  it('applies blast_radius penalty per violation', () => {
    const result = engine.score(
      baseInput({ blastRadiusViolations: 1 }),
      PROFILE_WITH_BLAST_RADIUS,
      { wasFirstTryPass: false },
    );
    expect(result.penalties.blast_radius).toBeCloseTo(0.15);
  });

  it('caps blast_radius penalty at the profile cap even with many violations', () => {
    const result = engine.score(
      baseInput({ blastRadiusViolations: 10 }), // 10 * 0.15 = 1.5, capped to 0.30
      PROFILE_WITH_BLAST_RADIUS,
      { wasFirstTryPass: false },
    );
    expect(result.penalties.blast_radius).toBe(0.3);
  });

  // Doc §6.4: penalties.overtime: {per_10min_over: 0.02, cap: 0.10}.
  const PROFILE_WITH_OVERTIME = {
    ...GUIDED_LAB_DEFAULT_PROFILE,
    penalties: {
      ...GUIDED_LAB_DEFAULT_PROFILE.penalties,
      overtime: { per10MinOver: 0.02, cap: 0.1 },
    },
  };

  it('applies no overtime penalty when active time is at or under the estimate', () => {
    const result = engine.score(
      baseInput({ activeMinutes: 35, estimatedMinutes: 35 }),
      PROFILE_WITH_OVERTIME,
      { wasFirstTryPass: false },
    );
    expect(result.penalties.overtime).toBe(0);
  });

  it('applies overtime penalty per full 10 minutes over the estimate', () => {
    const result = engine.score(
      baseInput({ activeMinutes: 55, estimatedMinutes: 35 }), // 20 min over -> 2 * 0.02
      PROFILE_WITH_OVERTIME,
      { wasFirstTryPass: false },
    );
    expect(result.penalties.overtime).toBeCloseTo(0.04);
  });

  it('does not count a partial 10-minute block over the estimate', () => {
    const result = engine.score(
      baseInput({ activeMinutes: 44, estimatedMinutes: 35 }), // 9 min over -> 0 full blocks
      PROFILE_WITH_OVERTIME,
      { wasFirstTryPass: false },
    );
    expect(result.penalties.overtime).toBe(0);
  });

  it('caps overtime penalty at the profile cap for a very long overrun', () => {
    const result = engine.score(
      baseInput({ activeMinutes: 200, estimatedMinutes: 35 }), // 165 min over -> 16 * 0.02 = 0.32, capped to 0.10
      PROFILE_WITH_OVERTIME,
      { wasFirstTryPass: false },
    );
    expect(result.penalties.overtime).toBe(0.1);
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

  // sp.production-sim.default unit coverage. Previously this profile had
  // only integration-level coverage (test/integration/evaluation.
  // integration.spec.ts's real-Postgres profile-selection test) — these
  // exercise ScoringEngineService.score() directly against the real,
  // registered profile object (not a synthetic profile built by
  // spreading GUIDED_LAB_DEFAULT_PROFILE, unlike the blast_radius/
  // overtime tests above, since PRODUCTION_SIM_DEFAULT_PROFILE already
  // configures both).
  describe('PRODUCTION_SIM_DEFAULT_PROFILE', () => {
    it('scores a clean production-sim attempt using troubleshooting/technical_implementation/reliability, not guided-lab criteria', () => {
      const result = engine.score(
        baseInput({
          diagnosticEfficiencyGoodCount: 4,
          diagnosticEfficiencyBadCount: 0,
        }),
        PRODUCTION_SIM_DEFAULT_PROFILE,
        { wasFirstTryPass: true },
      );
      expect(result.passed).toBe(true);
      // The three PRODUCTION_SIM-specific criterion keys must be present...
      expect(result.criteria).toHaveProperty('troubleshooting');
      expect(result.criteria).toHaveProperty('technical_implementation');
      expect(result.criteria).toHaveProperty('reliability');
      // ...and the guided-lab-only criteria must NOT be, confirming the
      // right profile's weights (not just its id) actually drove scoring.
      expect(result.criteria).not.toHaveProperty('task_completion');
      expect(result.criteria).not.toHaveProperty('efficiency');
    });

    it("technical_implementation and reliability alias technical_correctness's validator-pass-rate signal (doc §6.4: no dedicated computation specified for either yet)", () => {
      const result = engine.score(baseInput(), PRODUCTION_SIM_DEFAULT_PROFILE, {
        wasFirstTryPass: false,
      });
      // CRITERION_REGISTRY registers both as direct aliases of
      // fn.technical_correctness.v1 (criteria.ts) — asserting the
      // aliasing behavior end-to-end through the engine, not just that
      // the registry entry exists, since a scoring-engine-level bug
      // could break the alias even if the registry itself looks correct.
      expect(result.criteria.technical_implementation.value).toBeCloseTo(
        result.criteria.reliability.value,
      );
    });

    it('troubleshooting criterion blends diagnostic_efficiency and hypothesis_ordering (fn.troubleshooting.v1)', () => {
      const goodDiagnosis = engine.score(
        baseInput({
          diagnosticEfficiencyGoodCount: 5,
          diagnosticEfficiencyBadCount: 0,
        }),
        PRODUCTION_SIM_DEFAULT_PROFILE,
        { wasFirstTryPass: false },
      );
      const badDiagnosis = engine.score(
        baseInput({
          diagnosticEfficiencyGoodCount: 0,
          diagnosticEfficiencyBadCount: 5,
        }),
        PRODUCTION_SIM_DEFAULT_PROFILE,
        { wasFirstTryPass: false },
      );
      // A learner who used only good diagnostic actions (kubectl
      // describe/logs/events) must score higher on troubleshooting than
      // one who used only bad ones (kubectl delete pod --all,
      // rollout restart before diagnosing) — the whole point of doc
      // §3.3's diagnostic_efficiency signal.
      expect(goodDiagnosis.criteria.troubleshooting.value).toBeGreaterThan(
        badDiagnosis.criteria.troubleshooting.value,
      );
    });

    it('applies blast_radius penalty (configured in the real profile, not a synthetic test double)', () => {
      const result = engine.score(
        baseInput({ blastRadiusViolations: 2 }),
        PRODUCTION_SIM_DEFAULT_PROFILE,
        { wasFirstTryPass: false },
      );
      // 2 * 0.15 = 0.30, which is also the profile's own cap — asserts
      // against PRODUCTION_SIM_DEFAULT_PROFILE.penalties.blastRadius
      // directly rather than hardcoding the expected number, so this
      // test can't silently drift from the real profile's configured
      // values.
      const expected = Math.min(
        2 * PRODUCTION_SIM_DEFAULT_PROFILE.penalties!.blastRadius!.perViolation,
        PRODUCTION_SIM_DEFAULT_PROFILE.penalties!.blastRadius!.cap,
      );
      expect(result.penalties.blast_radius).toBeCloseTo(expected);
    });

    it('applies overtime penalty (configured in the real profile)', () => {
      const result = engine.score(
        baseInput({ activeMinutes: 75, estimatedMinutes: 45 }), // 30 min over -> 3 full 10-min blocks
        PRODUCTION_SIM_DEFAULT_PROFILE,
        { wasFirstTryPass: false },
      );
      const expected = Math.min(
        3 * PRODUCTION_SIM_DEFAULT_PROFILE.penalties!.overtime!.per10MinOver,
        PRODUCTION_SIM_DEFAULT_PROFILE.penalties!.overtime!.cap,
      );
      expect(result.penalties.overtime).toBeCloseTo(expected);
    });

    it('has no hints penalty configured (production sims use a real penalty ladder elsewhere, not the guided-lab hint system)', () => {
      const result = engine.score(
        baseInput({ hintPenaltyTotal: 0.5 }),
        PRODUCTION_SIM_DEFAULT_PROFILE,
        { wasFirstTryPass: false },
      );
      expect(result.penalties.hints).toBeUndefined();
    });

    it('guard prevents penalties from dragging an otherwise-passing production-sim score below the floor', () => {
      const result = engine.score(
        baseInput({
          resetCount: 10,
          retryIndex: 10,
          blastRadiusViolations: 10,
          activeMinutes: 200,
          estimatedMinutes: 45,
        }),
        PRODUCTION_SIM_DEFAULT_PROFILE,
        { wasFirstTryPass: false },
      );
      expect(result.finalScore).toBeGreaterThanOrEqual(
        PRODUCTION_SIM_DEFAULT_PROFILE.guards!.minAfterPenalties!,
      );
    });

    it('fails a production-sim attempt where a required task did not pass', () => {
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
        PRODUCTION_SIM_DEFAULT_PROFILE,
        { wasFirstTryPass: false },
      );
      expect(result.passed).toBe(false);
      expect(result.finalScore).toBeLessThan(
        PRODUCTION_SIM_DEFAULT_PROFILE.passThreshold,
      );
    });
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
