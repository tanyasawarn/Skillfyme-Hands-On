import { weightedKappa, type RatingPair } from './weighted-kappa';

const LEVELS = [1, 2, 3, 4];

describe('weightedKappa (Phase 3 rubric calibration — 1.9)', () => {
  it('returns kappa=1 for perfect agreement', () => {
    const pairs: RatingPair[] = [
      [1, 1],
      [2, 2],
      [3, 3],
      [4, 4],
      [2, 2],
      [3, 3],
    ];
    const r = weightedKappa(pairs, LEVELS);
    expect(r.kappa).toBeCloseTo(1, 10);
    expect(r.exactMatch).toBe(1);
    expect(r.meanAbsError).toBe(0);
    expect(r.n).toBe(6);
  });

  it('a consistent one-level bias scores modestly — not "good agreement"', () => {
    // grader one level low everywhere. This is a systematic bias, not
    // noise: weighted kappa correctly discounts it heavily because the
    // grader and reference marginals barely overlap (grader in {1,2,3},
    // reference in {2,3,4}). A real calibration run showing this pattern
    // should NOT pass the 0.6 gate — the rubric or prompt needs work.
    const pairs: RatingPair[] = [
      [1, 2],
      [2, 3],
      [3, 4],
      [1, 2],
      [2, 3],
      [3, 4],
    ];
    const r = weightedKappa(pairs, LEVELS);
    expect(r.exactMatch).toBe(0);
    expect(r.meanAbsError).toBe(1);
    expect(r.kappa).toBeGreaterThan(0); // still better than pure chance
    expect(r.kappa).toBeLessThan(0.6); // but nowhere near the trust threshold
  });

  it('mixed small errors around the reference clear the 0.6 trust gate', () => {
    // grader agrees on most, off-by-one on a few, no systematic direction
    const pairs: RatingPair[] = [
      [1, 1],
      [2, 2],
      [3, 3],
      [4, 4],
      [2, 3],
      [4, 3],
      [1, 1],
      [3, 3],
      [4, 4],
      [2, 2],
    ];
    const r = weightedKappa(pairs, LEVELS);
    expect(r.kappa).toBeGreaterThan(0.6);
    expect(r.kappa).toBeLessThan(1);
  });

  it('penalises large disagreements far more than small ones', () => {
    const offByOne: RatingPair[] = [
      [1, 2],
      [2, 3],
      [3, 4],
      [4, 3],
    ];
    const offByThree: RatingPair[] = [
      [1, 4],
      [2, 4],
      [4, 1],
      [3, 1],
    ];
    const a = weightedKappa(offByOne, LEVELS).kappa;
    const b = weightedKappa(offByThree, LEVELS).kappa;
    expect(a).toBeGreaterThan(b);
  });

  it('returns kappa <= 0 when agreement is no better than chance', () => {
    // grader ignores the reference entirely (always 3), references vary
    const pairs: RatingPair[] = [
      [3, 1],
      [3, 2],
      [3, 3],
      [3, 4],
      [3, 1],
      [3, 4],
    ];
    const r = weightedKappa(pairs, LEVELS);
    // degenerate grader marginal -> expected disagreement collapses; our
    // convention returns 0 (not a spurious positive) when there is
    // observed disagreement but none expected.
    expect(r.kappa).toBeLessThanOrEqual(0);
  });

  it('matches a hand-computed weighted kappa on a small mixed set', () => {
    // 4 pairs, 3-level scale [1,2,3], maxDist=2, linear weights.
    //   (1,1) (2,2) (2,3) (3,2)
    // observed disagreement = (0 + 0 + 0.5 + 0.5)/4 * ... computed below.
    const levels = [1, 2, 3];
    const pairs: RatingPair[] = [
      [1, 1],
      [2, 2],
      [2, 3],
      [3, 2],
    ];
    // grader marginal: 1->1, 2->2, 3->1  => p(g=1)=.25 p(g=2)=.5 p(g=3)=.25
    // ref marginal:    1->1, 2->2, 3->1  => p(r=1)=.25 p(r=2)=.5 p(r=3)=.25
    // weight(i,j)=|i-j|/2
    // observed weighted disagreement:
    //   (1,1)->0  (2,2)->0  (2,3)->0.5*(1/4)  (3,2)->0.5*(1/4)  => 0.25
    // expected weighted disagreement = sum_ij w(i,j)*p(g=i)*p(r=j)
    //   pairs contributing: (1,2)&(2,1):0.5 each; (1,3)&(3,1):1.0 each; (2,3)&(3,2):0.5 each
    //   = 2*[0.5*(.25*.5)] + 2*[1.0*(.25*.25)] + 2*[0.5*(.5*.25)]
    //   = 2*0.0625 + 2*0.0625 + 2*0.0625 = 0.375
    // kappa = 1 - 0.25/0.375 = 1 - 0.6667 = 0.3333
    const r = weightedKappa(pairs, levels);
    expect(r.kappa).toBeCloseTo(1 / 3, 6);
    expect(r.exactMatch).toBe(0.5);
    expect(r.meanAbsError).toBe(0.5);
  });

  it('returns NaN metrics for an empty set (no crash)', () => {
    const r = weightedKappa([], LEVELS);
    expect(r.n).toBe(0);
    expect(Number.isNaN(r.kappa)).toBe(true);
  });

  it('throws when a rating references a level outside the scale', () => {
    expect(() => weightedKappa([[1, 5]], LEVELS)).toThrow(/outside/);
  });

  it('throws for a degenerate 1-level scale', () => {
    expect(() => weightedKappa([[1, 1]], [1])).toThrow(/at least 2 levels/);
  });
});
