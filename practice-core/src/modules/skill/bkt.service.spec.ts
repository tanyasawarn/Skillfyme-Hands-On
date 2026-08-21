import { BktService } from './bkt.service';

describe('BktService', () => {
  const bkt = new BktService();
  const defaultParams = {
    pInit: 0.15,
    pTransit: 0.25,
    pSlip: 0.1,
    pGuess: 0.08,
  };

  it('increases mastery on a clean first-try pass', () => {
    const result = bkt.update({
      priorP: 0.15,
      params: defaultParams,
      score: 0.9,
      weight: 1.0,
      passThreshold: 0.65,
      difficultyAdjust: 0,
      wasGenuineAttempt: true,
    });
    expect(result.pAfter).toBeGreaterThan(result.pBefore);
    expect(result.delta).toBeGreaterThan(0);
  });

  it('decreases mastery on a genuine failed attempt', () => {
    const result = bkt.update({
      priorP: 0.5,
      params: defaultParams,
      score: 0.1,
      weight: 1.0,
      passThreshold: 0.65,
      difficultyAdjust: 0,
      wasGenuineAttempt: true,
    });
    expect(result.pAfter).toBeLessThan(result.pBefore);
  });

  it('applies zero positive evidence for assisted (non-genuine) attempts per §7.5', () => {
    const result = bkt.update({
      priorP: 0.4,
      params: defaultParams,
      score: 0.95,
      weight: 1.0,
      passThreshold: 0.65,
      difficultyAdjust: 0,
      wasGenuineAttempt: false,
    });
    // No transit applied; pAfter should equal the prior (no learning credit).
    expect(result.pAfter).toBeCloseTo(0.4, 5);
  });

  it('weighs a hard-activity pass as stronger evidence than an easy-activity pass (§2.4 step 2)', () => {
    const easyPass = bkt.update({
      priorP: 0.3,
      params: defaultParams,
      score: 0.9,
      weight: 1.0,
      passThreshold: 0.65,
      difficultyAdjust: 0, // activity Elo == learner Elo
      wasGenuineAttempt: true,
    });
    const hardPass = bkt.update({
      priorP: 0.3,
      params: defaultParams,
      score: 0.9,
      weight: 1.0,
      passThreshold: 0.65,
      difficultyAdjust: 0.4, // activity substantially harder than learner
      wasGenuineAttempt: true,
    });
    expect(hardPass.delta).toBeGreaterThan(easyPass.delta);
  });

  it('weighs a hard-activity fail as weaker (less negative) evidence than an easy-activity fail', () => {
    const easyFail = bkt.update({
      priorP: 0.6,
      params: defaultParams,
      score: 0.1,
      weight: 1.0,
      passThreshold: 0.65,
      difficultyAdjust: 0,
      wasGenuineAttempt: true,
    });
    const hardFail = bkt.update({
      priorP: 0.6,
      params: defaultParams,
      score: 0.1,
      weight: 1.0,
      passThreshold: 0.65,
      difficultyAdjust: 0.4,
      wasGenuineAttempt: true,
    });
    expect(hardFail.delta).toBeGreaterThan(easyFail.delta); // less negative
  });

  it('reproduces the direction of the doc §2.7 worked example: 3 failed attempts drop mastery 0.48 -> 0.19', () => {
    let p = 0.48;
    for (let i = 0; i < 3; i++) {
      const result = bkt.update({
        priorP: p,
        params: defaultParams,
        score: 0.15, // consistent poor performance, max hint ladder reached
        weight: 1.0,
        passThreshold: 0.65,
        difficultyAdjust: 0.2, // sim was harder than learner's k8s Elo (1520 vs 1280)
        wasGenuineAttempt: true,
      });
      p = result.pAfter;
    }
    // We don't expect to hit the doc's exact 0.19 (the doc doesn't specify
    // enough parameters to reproduce it bit-for-bit), but the qualitative
    // claim -- three failures substantially erode mastery -- must hold.
    expect(p).toBeLessThan(0.48);
    expect(p).toBeLessThan(0.35);
  });

  it('band boundaries match doc §2.4 table exactly', () => {
    expect(bkt.bandFor(0.29)).toBe('Novice');
    expect(bkt.bandFor(0.3)).toBe('Developing');
    expect(bkt.bandFor(0.54)).toBe('Developing');
    expect(bkt.bandFor(0.55)).toBe('Competent');
    expect(bkt.bandFor(0.74)).toBe('Competent');
    expect(bkt.bandFor(0.75)).toBe('Proficient');
    expect(bkt.bandFor(0.89)).toBe('Proficient');
    expect(bkt.bandFor(0.9)).toBe('Mastered');
  });

  it('decays mastery toward pInit over time, not toward zero', () => {
    const lastEvidence = new Date('2026-01-01T00:00:00Z');
    const now = new Date('2026-05-01T00:00:00Z'); // ~120 days later == one half-life
    const decayed = bkt.decayedMastery(0.9, 0.15, lastEvidence, 120, now);
    // After one half-life, should be roughly halfway back to pInit.
    expect(decayed).toBeCloseTo(0.15 + (0.9 - 0.15) * 0.5, 2);
    expect(decayed).toBeGreaterThan(0.15); // never decays below the prior
  });
});
