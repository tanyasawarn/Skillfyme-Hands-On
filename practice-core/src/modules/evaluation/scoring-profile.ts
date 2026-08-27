/** Doc §6.4 scoring profile shape, YAML example transcribed for sp.guided-lab.default. */
export interface ScoringProfile {
  id: string;
  version: number;
  passThreshold: number;
  weights: Record<string, number>;
  penalties?: {
    hints?: { perLevel: Record<string, number>; cap: number };
    resets?: { perReset: number; cap: number };
    retries?: { formulaCoefficient: number; cap: number };
    /** Doc §3.3 worked example: penalties.blast_radius: {per_violation: 0.15, cap: 0.30}. */
    blastRadius?: { perViolation: number; cap: number };
    /** Doc §6.4: overtime: {per_10min_over: 0.02, cap: 0.10}. */
    overtime?: { per10MinOver: number; cap: number };
  };
  bonuses?: {
    firstTryPass?: number;
  };
  guards?: {
    minAfterPenalties?: number;
  };
}

/**
 * Doc §3.2 worked example's scoring.overrides for lab.k8s.deploy-node-app:
 * technical_correctness .70, task_completion .20, efficiency .10.
 */
export const GUIDED_LAB_DEFAULT_PROFILE: ScoringProfile = {
  id: 'sp.guided-lab.default',
  version: 1,
  passThreshold: 0.7,
  weights: {
    technical_correctness: 0.7,
    task_completion: 0.2,
    efficiency: 0.1,
  },
  penalties: {
    hints: {
      perLevel: { level_1: 0.01, level_2: 0.03, level_3: 0.08 },
      cap: 0.2,
    },
    resets: { perReset: 0.02, cap: 0.06 },
    retries: { formulaCoefficient: 0.05, cap: 0.15 },
  },
  bonuses: {
    firstTryPass: 0.02,
  },
  guards: {
    // doc §6.4: "penalties can't take a working solution to zero"
    minAfterPenalties: 0.3,
  },
};

/**
 * Doc §6.4's named weights: troubleshooting .30, technical_implementation
 * .30, reliability .15, security .15, documentation .10. Only 3 of those
 * 5 have a real, doc-derived signal built today (troubleshooting =
 * diagnostic_efficiency + hypothesis_ordering blend; technical_
 * implementation and reliability both alias technical_correctness --
 * see CRITERION_REGISTRY's own doc comment on why, including how a
 * NO_REGRESSION validator authored into a task already feeds
 * "reliability" through that alias). security and documentation are
 * deliberately absent rather than faked: no security-specific validator
 * type is implemented (CLOUD_ASSERT/STATIC_ANALYSIS/IAC_STATE, doc
 * §11.3, are all still unimplemented per validation.go's own package
 * comment), and documentation (the incident-note artifact + AI rubric,
 * doc §6.5) is Task 8's AI Gateway work, stubbed but not yet wired into
 * scoring. Their combined .25 weight is folded into troubleshooting
 * (.30 -> .55) rather than silently dropped or arbitrarily spread thin
 * across the 3 real criteria -- troubleshooting is the criterion this
 * profile has the strongest, most doc-faithful signal for, so weighting
 * toward it is the most defensible redistribution until security/
 * documentation get real criteria of their own (at which point this
 * profile needs a version bump, doc §3.6's rubric-evolution story --
 * see EvaluationService's own comment on profile_version_id/
 * criterion_fn_versions_jsonb being what makes that safe).
 */
export const PRODUCTION_SIM_DEFAULT_PROFILE: ScoringProfile = {
  id: 'sp.production-sim.default',
  version: 1,
  passThreshold: 0.7,
  weights: {
    troubleshooting: 0.55,
    technical_implementation: 0.3,
    reliability: 0.15,
  },
  penalties: {
    resets: { perReset: 0.02, cap: 0.06 },
    retries: { formulaCoefficient: 0.05, cap: 0.15 },
    blastRadius: { perViolation: 0.15, cap: 0.3 },
    overtime: { per10MinOver: 0.02, cap: 0.1 },
  },
  bonuses: {
    firstTryPass: 0.02,
  },
  guards: {
    minAfterPenalties: 0.3,
  },
};
