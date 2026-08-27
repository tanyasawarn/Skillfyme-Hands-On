import { Injectable } from '@nestjs/common';

/** Doc §2.6: outcome of one learner-vs-activity "match". */
export type EloOutcome = 1 | 0.5 | 0;

export interface EloUpdateInput {
  learnerElo: number;
  activityElo: number;
  /** Attempt count for this learner in the activity's skill-cluster, before this match -- drives K_l decay. */
  learnerMatchCount: number;
  /** Total attempts recorded against this activity version, before this match -- drives the >500 K_a freeze. */
  activityAttemptCount: number;
  outcome: EloOutcome;
}

export interface EloUpdateResult {
  expectedPass: number;
  learnerElo: number;
  activityElo: number;
  /** (activityElo - learnerElo) / 400 from the learner's rating *before* this update -- the BktService difficultyAdjust input, doc §2.4 step 2. */
  difficultyAdjust: number;
}

const DEFAULT_RATING = 1200;
const K_LEARNER_START = 32;
const K_LEARNER_FLOOR = 12;
const K_LEARNER_DECAY_MATCHES = 20; // matches over which K_l decays 32 -> 12
const K_ACTIVITY = 4;
const ACTIVITY_FREEZE_ATTEMPT_COUNT = 500;

@Injectable()
export class EloService {
  /**
   * Doc §2.6: "Treat each attempt as a match: learner rating R_l vs
   * activity rating R_a."
   *   expected_pass = 1 / (1 + 10^((R_a - R_l)/400))
   *   R_l' = R_l + K_l * (outcome - expected_pass)
   *   R_a' = R_a - K_a * (outcome - expected_pass)
   * K_l decays 32 -> 12 with attempt count so ratings stabilise; K_a is
   * small (4) and frozen once an activity has >500 attempts so a cohort
   * of beginners doesn't wreck a calibrated activity.
   */
  update(input: EloUpdateInput): EloUpdateResult {
    const learnerElo = input.learnerElo;
    const activityElo = input.activityElo;

    const expectedPass =
      1 / (1 + Math.pow(10, (activityElo - learnerElo) / 400));
    const surprise = input.outcome - expectedPass;

    const kLearner = this.kLearnerFor(input.learnerMatchCount);
    const kActivity =
      input.activityAttemptCount > ACTIVITY_FREEZE_ATTEMPT_COUNT
        ? 0
        : K_ACTIVITY;

    return {
      expectedPass,
      learnerElo: learnerElo + kLearner * surprise,
      activityElo: activityElo - kActivity * surprise,
      difficultyAdjust: (activityElo - learnerElo) / 400,
    };
  }

  /** No prior rating in this skill-cluster -- doc §2.6 population default. */
  defaultRating(): number {
    return DEFAULT_RATING;
  }

  /**
   * Linear decay from K_LEARNER_START to K_LEARNER_FLOOR over
   * K_LEARNER_DECAY_MATCHES matches, then held at the floor -- "ratings
   * stabilise" without a discontinuous step change.
   */
  private kLearnerFor(matchCount: number): number {
    const progress = Math.min(matchCount / K_LEARNER_DECAY_MATCHES, 1);
    return K_LEARNER_START - (K_LEARNER_START - K_LEARNER_FLOOR) * progress;
  }
}
