import { Injectable } from '@nestjs/common';

/** Doc §2.4 four-parameter BKT model, per-skill. */
export interface BktParams {
  pInit: number;
  pTransit: number;
  pSlip: number;
  pGuess: number;
}

export interface BktUpdateInput {
  /** Current P(mastery) before this evidence, or pInit if no prior evidence. */
  priorP: number;
  params: BktParams;
  /** Raw activity score in [0,1]. */
  score: number;
  /** activity_skill.weight for this (activity, skill) pair. */
  weight: number;
  /** Scoring profile's pass_threshold, doc §2.4 step 1. */
  passThreshold: number;
  /**
   * Difficulty-adjust factor per §2.4 step 2: activity Elo vs learner Elo.
   * >0 means the activity was harder than the learner's rating (pass is
   * stronger evidence, fail is weaker); <0 the reverse. Caller computes
   * this from (activityElo - learnerElo) / 400 or similar; kept as a
   * pre-computed multiplier here so BktService has no Elo dependency.
   */
  difficultyAdjust: number;
  /** False for assisted/skipped tasks — doc §7.5: assisted tasks contribute zero positive evidence. */
  wasGenuineAttempt: boolean;
}

export interface BktUpdateResult {
  pBefore: number;
  pAfter: number;
  delta: number;
  evidenceStrength: number;
}

@Injectable()
export class BktService {
  /**
   * Doc §2.4 "Update on evidence" — implements steps 1-4 verbatim:
   *   1. Binarise with partial credit: e = w * (s - pass_threshold) / (1 - pass_threshold), clipped
   *   2. Difficulty-adjust p_guess/p_slip
   *   3. Bayes update P(L | evidence)
   *   4. Apply learning transit
   * Step 5 (persist with evidence id) is the repository's job, not this
   * service's — this function is pure so it's independently testable
   * against the doc's worked example (§2.7).
   */
  update(input: BktUpdateInput): BktUpdateResult {
    const {
      priorP,
      params,
      score,
      weight,
      passThreshold,
      difficultyAdjust,
      wasGenuineAttempt,
    } = input;

    // Step 1: binarise with partial credit, clipped to [-1, 1] then to [0,1]
    // for use as an evidence-strength multiplier on the Bayes update weight.
    const denom = 1 - passThreshold;
    const rawEvidence =
      denom > 0 ? (weight * (score - passThreshold)) / denom : 0;
    const evidenceStrength = clamp(rawEvidence, -1, 1);
    const passed = evidenceStrength >= 0;

    // Step 2: difficulty-adjust. Positive difficultyAdjust => activity was
    // harder than the learner => passing is stronger evidence (lower
    // effective p_guess), failing is weaker evidence (higher effective
    // p_slip). Adjustment is bounded so parameters never leave (0,1).
    const adjMagnitude = clamp(Math.abs(difficultyAdjust), 0, 0.5);
    const pGuess =
      difficultyAdjust > 0
        ? params.pGuess * (1 - adjMagnitude)
        : params.pGuess * (1 + adjMagnitude);
    const pSlip =
      difficultyAdjust > 0
        ? Math.min(params.pSlip * (1 + adjMagnitude), 0.9)
        : params.pSlip * (1 - adjMagnitude);

    // Step 3: Bayes update P(L | evidence). Standard BKT posterior, scaled
    // by |evidenceStrength| so partial credit produces a smaller update
    // than a clean pass/fail (the doc's "clipped" evidence strength is the
    // mechanism for weighting weak vs strong evidence into the same Bayes
    // step rather than a separate blending pass).
    const pPrior = clamp(priorP, 1e-6, 1 - 1e-6);
    const likelihoodGivenMastery = passed ? 1 - pSlip : pSlip;
    const likelihoodGivenNoMastery = passed ? pGuess : 1 - pGuess;
    const numerator = pPrior * likelihoodGivenMastery;
    const posterior =
      numerator / (numerator + (1 - pPrior) * likelihoodGivenNoMastery);

    // Blend posterior with prior by |evidenceStrength| so a barely-passing
    // score moves mastery less than a strong pass — this is the "partial
    // credit" the binarisation step existed to compute.
    const strength = Math.abs(evidenceStrength);
    const blended = pPrior + (posterior - pPrior) * strength;

    // Step 4: apply learning transit, only for genuine attempts (§7.5:
    // assisted/skipped tasks contribute zero positive evidence to BKT).
    const pAfter = wasGenuineAttempt
      ? blended + (1 - blended) * params.pTransit
      : pPrior; // no transit, no posterior movement for non-genuine attempts

    const clampedAfter = clamp(pAfter, 1e-6, 1 - 1e-6);

    return {
      pBefore: pPrior,
      pAfter: clampedAfter,
      delta: clampedAfter - pPrior,
      evidenceStrength,
    };
  }

  /**
   * Doc §2.4 decay: "computed lazily at read time from last_evidence_at ...
   * do not run a nightly job." Exponential half-life decay toward pInit
   * (mastery ages back toward the population prior, not to zero).
   */
  decayedMastery(
    pMastery: number,
    pInit: number,
    lastEvidenceAt: Date,
    halfLifeDays: number,
    now: Date = new Date(),
  ): number {
    const daysSince =
      (now.getTime() - lastEvidenceAt.getTime()) / (1000 * 60 * 60 * 24);
    if (daysSince <= 0) return pMastery;
    const decayFactor = Math.pow(0.5, daysSince / halfLifeDays);
    // Decays toward pInit, not toward 0 — an expert who stops practicing
    // doesn't regress below the population prior for that skill.
    return pInit + (pMastery - pInit) * decayFactor;
  }

  /** Doc §2.4 mastery bands table. */
  bandFor(
    pMastery: number,
  ): 'Novice' | 'Developing' | 'Competent' | 'Proficient' | 'Mastered' {
    if (pMastery < 0.3) return 'Novice';
    if (pMastery < 0.55) return 'Developing';
    if (pMastery < 0.75) return 'Competent';
    if (pMastery < 0.9) return 'Proficient';
    return 'Mastered';
  }
}

function clamp(value: number, min: number, max: number): number {
  return Math.min(max, Math.max(min, value));
}
