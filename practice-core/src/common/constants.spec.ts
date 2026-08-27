import {
  MasteryConstants,
  GrpcClientConstants,
  TimeoutConstants,
  EligibilityConstants,
} from './constants';

describe('constants', () => {
  it('MasteryConstants.REQUIRES_GATE_THRESHOLD matches doc §2.5 stage 2 (0.55)', () => {
    expect(MasteryConstants.REQUIRES_GATE_THRESHOLD).toBe(0.55);
  });

  it('GrpcClientConstants.DEFAULT_DEADLINE_MS matches the prior duplicated default (30s)', () => {
    expect(GrpcClientConstants.DEFAULT_DEADLINE_MS).toBe(30_000);
  });

  it('TimeoutConstants.WORKSPACE_FILE_OP_MS matches the prior duplicated timeout (10s)', () => {
    expect(TimeoutConstants.WORKSPACE_FILE_OP_MS).toBe(10_000);
  });

  // PLAN.md K12: the concurrent-environment quota comparison and its
  // user-facing error message used to be two independent literals in
  // eligibility.service.ts (`>= 1` and "1 active environment per
  // learner") -- consistent today only by coincidence. This pins the
  // single shared value so a future edit to one can't silently drift
  // from the other again.
  it('EligibilityConstants.MAX_CONCURRENT_ENVIRONMENTS_PER_LEARNER matches doc §4.4 ("one active environment per learner by default")', () => {
    expect(EligibilityConstants.MAX_CONCURRENT_ENVIRONMENTS_PER_LEARNER).toBe(
      1,
    );
  });
});
