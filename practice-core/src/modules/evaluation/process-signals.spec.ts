import {
  diagnosticEfficiencyV1,
  hypothesisOrderingV1,
  troubleshootingV1,
  type CriterionInput,
} from './criteria';

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

describe('diagnosticEfficiencyV1 (fn.diagnostic_efficiency.v1, doc §3.3 ratio half)', () => {
  it('scores 1.0 when no good/bad actions were observed at all (nothing to penalise)', () => {
    const result = diagnosticEfficiencyV1(baseInput());
    expect(result.value).toBe(1);
  });

  it('scores 1.0 when every observed action was good', () => {
    const result = diagnosticEfficiencyV1(
      baseInput({
        diagnosticEfficiencyGoodCount: 4,
        diagnosticEfficiencyBadCount: 0,
      }),
    );
    expect(result.value).toBe(1);
  });

  it('scores 0 when every observed action was bad', () => {
    const result = diagnosticEfficiencyV1(
      baseInput({
        diagnosticEfficiencyGoodCount: 0,
        diagnosticEfficiencyBadCount: 3,
      }),
    );
    expect(result.value).toBe(0);
  });

  it('scores the good/(good+bad) ratio for a mix', () => {
    const result = diagnosticEfficiencyV1(
      baseInput({
        diagnosticEfficiencyGoodCount: 3,
        diagnosticEfficiencyBadCount: 1,
      }),
    );
    expect(result.value).toBeCloseTo(0.75);
  });
});

describe('hypothesisOrderingV1 (fn.hypothesis_ordering.v1, doc line 2047 ordering half)', () => {
  const canonicalPath = [
    'kubectl get pods',
    'kubectl describe pod',
    'kubectl top pod',
    'kubectl get deploy',
  ];

  it('scores 1.0 when no faults with a canonical path were applied (nothing to order against)', () => {
    const result = hypothesisOrderingV1(baseInput());
    expect(result.value).toBe(1);
  });

  it('scores 1.0 for a perfect match of the canonical order', () => {
    const result = hypothesisOrderingV1(
      baseInput({
        canonicalDiagnosticPaths: [canonicalPath],
        commandSequence: [
          'kubectl get pods',
          'kubectl describe pod checkout-abc',
          'kubectl top pod checkout-abc',
          'kubectl get deploy -o yaml',
        ],
      }),
    );
    expect(result.value).toBe(1);
  });

  it('scores 0 for the exact reverse order', () => {
    const result = hypothesisOrderingV1(
      baseInput({
        canonicalDiagnosticPaths: [canonicalPath],
        commandSequence: [
          'kubectl get deploy -o yaml',
          'kubectl top pod checkout-abc',
          'kubectl describe pod checkout-abc',
          'kubectl get pods',
        ],
      }),
    );
    expect(result.value).toBe(0);
  });

  it('excludes steps the learner never investigated rather than penalising them as out-of-order', () => {
    // Only 2 of 4 canonical steps were run, but in the right relative order.
    const result = hypothesisOrderingV1(
      baseInput({
        canonicalDiagnosticPaths: [canonicalPath],
        commandSequence: [
          'kubectl get pods',
          'kubectl describe pod checkout-abc',
        ],
      }),
    );
    expect(result.value).toBe(1);
  });

  it('scores 1.0 (no comparable pairs) when fewer than 2 canonical steps were ever investigated', () => {
    const result = hypothesisOrderingV1(
      baseInput({
        canonicalDiagnosticPaths: [canonicalPath],
        commandSequence: ['kubectl get pods'],
      }),
    );
    expect(result.value).toBe(1);
    expect(result.explanation.reason).toMatch(/fewer than 2/);
  });

  it('averages scores across multiple applied faults', () => {
    const secondPath = ['curl the service', 'kubectl get endpoints'];
    const result = hypothesisOrderingV1(
      baseInput({
        canonicalDiagnosticPaths: [canonicalPath, secondPath],
        commandSequence: [
          // First fault: perfect order (score 1.0).
          'kubectl get pods',
          'kubectl describe pod',
          // Second fault: reverse order (score 0.0).
          'kubectl get endpoints checkout',
          'curl the service checkout',
        ],
      }),
    );
    expect(result.value).toBeCloseTo(0.5);
  });
});

describe('troubleshootingV1 (fn.troubleshooting.v1, doc §6.4 sp.production-sim.default criterion)', () => {
  it('is the average of diagnostic_efficiency and hypothesis_ordering', () => {
    const result = troubleshootingV1(
      baseInput({
        diagnosticEfficiencyGoodCount: 3,
        diagnosticEfficiencyBadCount: 1, // ratio = 0.75
        // No canonicalDiagnosticPaths -> hypothesis_ordering defaults to 1.0
      }),
    );
    expect(result.value).toBeCloseTo((0.75 + 1) / 2);
    expect(result.explanation.diagnostic_efficiency).toBeCloseTo(0.75);
    expect(result.explanation.hypothesis_ordering).toBe(1);
  });

  it('scores 1.0 when both halves have no evidence to penalise (a GUIDED_LAB-shaped input)', () => {
    const result = troubleshootingV1(baseInput());
    expect(result.value).toBe(1);
  });

  it('a bad ratio alone is not fully compensated by a perfect ordering', () => {
    const result = troubleshootingV1(
      baseInput({
        diagnosticEfficiencyGoodCount: 0,
        diagnosticEfficiencyBadCount: 4, // ratio = 0
        canonicalDiagnosticPaths: [
          ['kubectl get pods', 'kubectl describe pod'],
        ],
        commandSequence: ['kubectl get pods', 'kubectl describe pod'], // perfect order = 1.0
      }),
    );
    expect(result.value).toBeCloseTo(0.5); // (0 + 1) / 2, not 1.0
  });
});
