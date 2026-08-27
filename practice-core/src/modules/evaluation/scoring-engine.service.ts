import { Injectable } from '@nestjs/common';
import { CRITERION_REGISTRY, type CriterionInput } from './criteria';
import type { ScoringProfile } from './scoring-profile';

export interface ScoreBreakdown {
  finalScore: number;
  passed: boolean;
  criteria: Record<
    string,
    { value: number; weight: number; explanation: Record<string, unknown> }
  >;
  penalties: Record<string, number>;
  bonuses: Record<string, number>;
  criterionFnVersions: Record<string, string>;
}

/**
 * Doc §6.4: "final_score, per-criterion breakdown, feedback." Pure
 * function over (criteria input, profile) -- no DB access, so it's
 * trivially unit-testable and safely re-runnable for the re-scoring
 * flow doc §3.6/§6.4 describes ("POST /admin/rescore ... supported
 * operation").
 */
@Injectable()
export class ScoringEngineService {
  score(
    input: CriterionInput,
    profile: ScoringProfile,
    opts: { wasFirstTryPass: boolean },
  ): ScoreBreakdown {
    const criteria: ScoreBreakdown['criteria'] = {};
    const criterionFnVersions: Record<string, string> = {};
    let weightedSum = 0;

    for (const [criterionKey, weight] of Object.entries(profile.weights)) {
      const registered = CRITERION_REGISTRY[criterionKey];
      if (!registered) {
        throw new Error(
          `unknown criterion "${criterionKey}" in profile ${profile.id} -- not in CRITERION_REGISTRY`,
        );
      }
      const result = registered.fn(input);
      criteria[criterionKey] = {
        value: result.value,
        weight,
        explanation: result.explanation,
      };
      criterionFnVersions[criterionKey] = registered.version;
      weightedSum += result.value * weight;
    }

    const penalties: Record<string, number> = {};
    let totalPenalty = 0;

    if (profile.penalties?.hints) {
      // input.hintPenaltyTotal is already the sum of authored per-hint
      // penalties (accrued by attempt.hint_penalty_total as hints are
      // requested) -- the profile's hints config caps that accrued total
      // rather than recomputing it, since the per-hint cost is authored
      // per-activity (doc §3.2 hints[].penalty), not fixed per profile.
      const hintPenalty = Math.min(
        input.hintPenaltyTotal,
        profile.penalties.hints.cap,
      );
      penalties.hints = hintPenalty;
      totalPenalty += hintPenalty;
    }
    if (profile.penalties?.resets) {
      const resetPenalty = Math.min(
        input.resetCount * profile.penalties.resets.perReset,
        profile.penalties.resets.cap,
      );
      penalties.resets = resetPenalty;
      totalPenalty += resetPenalty;
    }
    if (profile.penalties?.retries) {
      const retryPenalty = Math.min(
        input.retryIndex * profile.penalties.retries.formulaCoefficient,
        profile.penalties.retries.cap,
      );
      penalties.retries = retryPenalty;
      totalPenalty += retryPenalty;
    }
    if (profile.penalties?.blastRadius) {
      // Doc §3.3: "A learner who fixes the 502 by deleting the namespace
      // and redeploying from scratch technically restores service and
      // should not score well" -- this is the mechanism that makes that
      // true regardless of how well every other criterion scores.
      const blastRadiusPenalty = Math.min(
        input.blastRadiusViolations *
          profile.penalties.blastRadius.perViolation,
        profile.penalties.blastRadius.cap,
      );
      penalties.blast_radius = blastRadiusPenalty;
      totalPenalty += blastRadiusPenalty;
    }
    if (profile.penalties?.overtime) {
      // Doc §6.4: "overtime: {per_10min_over: 0.02, cap: 0.10}" -- a
      // production incident that takes meaningfully longer than
      // estimated_minutes costs real (simulated) business impact, on
      // top of whatever efficiency already reflects; distinct penalty,
      // not a duplicate of the efficiency criterion, which caps out at
      // 0 rather than continuing to subtract.
      const overMinutes = Math.max(
        0,
        input.activeMinutes - input.estimatedMinutes,
      );
      const overtimePenalty = Math.min(
        Math.floor(overMinutes / 10) * profile.penalties.overtime.per10MinOver,
        profile.penalties.overtime.cap,
      );
      penalties.overtime = overtimePenalty;
      totalPenalty += overtimePenalty;
    }

    const bonuses: Record<string, number> = {};
    let totalBonus = 0;
    if (profile.bonuses?.firstTryPass && opts.wasFirstTryPass) {
      bonuses.first_try_pass = profile.bonuses.firstTryPass;
      totalBonus += profile.bonuses.firstTryPass;
    }

    let finalScore = weightedSum - totalPenalty + totalBonus;

    // Doc §6.4 guard: "penalties can't take a working solution to zero."
    // Only applies the floor when the unpenalised score would have passed
    // -- a guard against penalties, not a general score floor for
    // genuinely failing attempts.
    const guard = profile.guards?.minAfterPenalties;
    if (
      guard !== undefined &&
      weightedSum >= profile.passThreshold &&
      finalScore < guard
    ) {
      finalScore = guard;
    }

    finalScore = Math.min(1, Math.max(0, finalScore));

    return {
      finalScore,
      passed: finalScore >= profile.passThreshold,
      criteria,
      penalties,
      bonuses,
      criterionFnVersions,
    };
  }
}
