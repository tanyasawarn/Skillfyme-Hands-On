import { EloService } from './elo.service';

describe('EloService — doc §2.6 difficulty calibration', () => {
  const elo = new EloService();

  it('expected_pass is 0.5 when learner and activity ratings are equal', () => {
    const result = elo.update({
      learnerElo: 1200,
      activityElo: 1200,
      learnerMatchCount: 0,
      activityAttemptCount: 0,
      outcome: 1,
    });
    expect(result.expectedPass).toBeCloseTo(0.5, 5);
  });

  it('expected_pass drops below 0.5 when the activity is rated harder than the learner', () => {
    // doc worked example: sim Elo 1520, learner Elo 1280.
    const result = elo.update({
      learnerElo: 1280,
      activityElo: 1520,
      learnerMatchCount: 5,
      activityAttemptCount: 100,
      outcome: 0,
    });
    expect(result.expectedPass).toBeLessThan(0.5);
  });

  it('a first-try pass (outcome=1) raises learner rating and lowers activity rating when the learner over-performs expectation', () => {
    const result = elo.update({
      learnerElo: 1200,
      activityElo: 1200,
      learnerMatchCount: 0,
      activityAttemptCount: 0,
      outcome: 1,
    });
    expect(result.learnerElo).toBeGreaterThan(1200);
    expect(result.activityElo).toBeLessThan(1200);
    // K_l=32 at 0 prior matches, surprise = 1 - 0.5 = 0.5 -> +16
    expect(result.learnerElo).toBeCloseTo(1216, 5);
    // K_a=4, surprise=0.5 -> -2
    expect(result.activityElo).toBeCloseTo(1198, 5);
  });

  it('a fail (outcome=0) lowers learner rating and raises activity rating', () => {
    const result = elo.update({
      learnerElo: 1200,
      activityElo: 1200,
      learnerMatchCount: 0,
      activityAttemptCount: 0,
      outcome: 0,
    });
    expect(result.learnerElo).toBeLessThan(1200);
    expect(result.activityElo).toBeGreaterThan(1200);
  });

  it('a retry pass (outcome=0.5) moves ratings less than a first-try pass, same direction', () => {
    // Unequal ratings so expectedPass != 0.5 and outcome=0.5 is a genuine
    // (smaller) surprise, not a zero-surprise no-op.
    const firstTry = elo.update({
      learnerElo: 1200,
      activityElo: 1300,
      learnerMatchCount: 0,
      activityAttemptCount: 0,
      outcome: 1,
    });
    const retry = elo.update({
      learnerElo: 1200,
      activityElo: 1300,
      learnerMatchCount: 0,
      activityAttemptCount: 0,
      outcome: 0.5,
    });
    expect(retry.learnerElo).toBeGreaterThan(1200);
    expect(retry.learnerElo).toBeLessThan(firstTry.learnerElo);
  });

  it('K_l decays from 32 toward 12 as learnerMatchCount grows', () => {
    const early = elo.update({
      learnerElo: 1200,
      activityElo: 1200,
      learnerMatchCount: 0,
      activityAttemptCount: 0,
      outcome: 1,
    });
    const late = elo.update({
      learnerElo: 1200,
      activityElo: 1200,
      learnerMatchCount: 100, // well past the decay window
      activityAttemptCount: 0,
      outcome: 1,
    });
    const earlyDelta = early.learnerElo - 1200;
    const lateDelta = late.learnerElo - 1200;
    expect(lateDelta).toBeLessThan(earlyDelta);
    // floor: surprise=0.5, K_l=12 -> +6
    expect(late.learnerElo).toBeCloseTo(1206, 5);
  });

  it('K_a freezes to 0 once activityAttemptCount exceeds 500, so activityElo does not move', () => {
    const result = elo.update({
      learnerElo: 1200,
      activityElo: 1500,
      learnerMatchCount: 10,
      activityAttemptCount: 501,
      outcome: 1,
    });
    expect(result.activityElo).toBe(1500);
  });

  it('activityElo still moves at exactly 500 attempts (freeze is only for >500)', () => {
    const result = elo.update({
      learnerElo: 1200,
      activityElo: 1500,
      learnerMatchCount: 10,
      activityAttemptCount: 500,
      outcome: 1,
    });
    expect(result.activityElo).not.toBe(1500);
  });

  it('difficultyAdjust is (activityElo - learnerElo) / 400, computed from the pre-update ratings', () => {
    const result = elo.update({
      learnerElo: 1280,
      activityElo: 1520,
      learnerMatchCount: 5,
      activityAttemptCount: 10,
      outcome: 0,
    });
    expect(result.difficultyAdjust).toBeCloseTo((1520 - 1280) / 400, 5);
  });

  it('defaultRating returns the population default for a learner/activity with no prior rating', () => {
    expect(elo.defaultRating()).toBe(1200);
  });
});
