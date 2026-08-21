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
