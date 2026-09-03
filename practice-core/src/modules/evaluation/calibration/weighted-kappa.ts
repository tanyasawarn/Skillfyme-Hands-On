/**
 * Cohen's weighted kappa (linear weights) for ordinal ratings — the
 * agreement statistic the rubric calibration harness gates on (doc §6.5
 * rule 36; PLAN_PHASE3_PROJECTS.md 1.9). Lives in src/ (not scripts/) so
 * it is unit-tested: the calibration decision — trust a rubric's AI
 * scores or keep them 100%-human-reviewed — rides on this number.
 *
 * Linear weights: disagreement of one level costs 1/(k-1), two levels
 * 2/(k-1), etc. A grader that is off by one on an ordinal scale is
 * penalised, but far less than one that is off by three.
 */

/** One [graderLevel, referenceLevel] pair. */
export type RatingPair = readonly [number, number];

export interface KappaResult {
  /** weighted kappa in [-1, 1]; 1 = perfect, 0 = chance, <0 = worse than chance. */
  kappa: number;
  /** fraction of pairs where grader level === reference level. */
  exactMatch: number;
  /** mean absolute difference in level. */
  meanAbsError: number;
  n: number;
}

/**
 * @param pairs  [graderLevel, referenceLevel] observations
 * @param levels the ordinal level scale, e.g. [1, 2, 3, 4]; order matters
 *               (used for the distance metric), values must cover every
 *               level that appears in `pairs`.
 */
export function weightedKappa(
  pairs: readonly RatingPair[],
  levels: readonly number[],
): KappaResult {
  const n = pairs.length;
  if (n === 0) {
    return { kappa: NaN, exactMatch: NaN, meanAbsError: NaN, n: 0 };
  }
  const k = levels.length;
  if (k < 2) {
    throw new Error('weightedKappa: need at least 2 levels');
  }
  const idx = new Map<number, number>(levels.map((lv, i) => [lv, i]));
  const maxDist = k - 1;

  const observed: number[][] = Array.from({ length: k }, () =>
    new Array<number>(k).fill(0),
  );
  const rowMarg = new Array<number>(k).fill(0);
  const colMarg = new Array<number>(k).fill(0);

  let exact = 0;
  let absErr = 0;
  for (const [g, r] of pairs) {
    const gi = idx.get(g);
    const ri = idx.get(r);
    if (gi === undefined || ri === undefined) {
      throw new Error(
        `weightedKappa: rating (${g}, ${r}) has a level outside [${levels.join(',')}]`,
      );
    }
    observed[gi][ri] += 1;
    rowMarg[gi] += 1;
    colMarg[ri] += 1;
    if (g === r) exact += 1;
    absErr += Math.abs(g - r);
  }

  const weight = (i: number, j: number): number => Math.abs(i - j) / maxDist;

  let obsDisagreement = 0;
  let expDisagreement = 0;
  for (let i = 0; i < k; i++) {
    for (let j = 0; j < k; j++) {
      const w = weight(i, j);
      obsDisagreement += w * (observed[i][j] / n);
      expDisagreement += w * ((rowMarg[i] / n) * (colMarg[j] / n));
    }
  }

  let kappa: number;
  if (expDisagreement === 0) {
    // no disagreement expected by chance (a marginal is degenerate);
    // kappa is 1 iff there is also no observed disagreement, else 0.
    kappa = obsDisagreement === 0 ? 1 : 0;
  } else {
    kappa = 1 - obsDisagreement / expDisagreement;
  }

  return {
    kappa,
    exactMatch: exact / n,
    meanAbsError: absErr / n,
    n,
  };
}
