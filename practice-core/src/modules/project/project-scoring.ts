import { Injectable } from '@nestjs/common';
import type { ProjectMilestoneKey } from '../../db/schema';

/**
 * Phase 3 (PLAN_PHASE3_PROJECTS.md 3.9 / B4). `sp.project.default` — the
 * milestone-weighted scoring roll-up for PROJECT mode.
 *
 * Unlike the signal-weighted GUIDED_LAB / PRODUCTION_SIM profiles
 * (scoring-profile.ts), a project's final score rolls up the five
 * milestone scores. §12.3's shape:
 *   - design / hardening / defence carry the rubric-heavy weight;
 *   - infra / implementation carry the deterministic-validator weight.
 *   - AI-derived contribution is capped at 40% of the total (R5).
 *   - mastery is updated across all mapped skills, weighted by the
 *     milestone scores (§12.3 POST).
 *
 * The `defence` component is not a milestone row — it comes from the
 * milestone-5 viva transcript score (rub.reasoning.v1, Stage 3.8). It is
 * passed in separately.
 */

export const SP_PROJECT_DEFAULT_ID = 'sp.project.default';

// Milestone weights. design + hardening + defence = the rubric-heavy
// half; infra + implementation = the deterministic-validator half.
// `final` (the acceptance milestone) folds into implementation's slice.
export const MILESTONE_WEIGHTS: Record<
  ProjectMilestoneKey | 'defence',
  number
> = {
  design: 0.2,
  infra: 0.15,
  implementation: 0.2,
  hardening: 0.2,
  final: 0.1,
  defence: 0.15,
};

// Which contributions are AI-derived, for the R5 40% cap. design +
// hardening rubric grades and the defence viva score are AI; infra +
// implementation + final are deterministic validators.
const AI_COMPONENTS = new Set<string>(['design', 'hardening', 'defence']);

export interface MilestoneScoreInput {
  key: ProjectMilestoneKey;
  /** milestone score in [0,1]; null ⇒ not reached / not gated */
  score: number | null;
  /** true when this milestone's score came (wholly or in part) from a rubric grade */
  aiDerived?: boolean;
}

export interface ProjectRollupInput {
  milestones: MilestoneScoreInput[];
  /** rub.reasoning.v1 transcript score in [0,1]; null ⇒ no viva yet */
  defenceScore: number | null;
  /** skills mapped to this activity and their activity_skill.weight */
  mappedSkills: Array<{ skillId: string; weight: number }>;
}

export interface ProjectRollupResult {
  finalScore: number; // [0,1]
  passThreshold: number;
  passed: boolean;
  /** per-component contribution to the final score (already weighted) */
  breakdown: Array<{
    component: string;
    rawScore: number;
    weight: number;
    weighted: number;
    aiDerived: boolean;
  }>;
  /** fraction of finalScore that came from AI-derived components (post-cap) */
  aiFraction: number;
  aiCapApplied: boolean;
  /** per-skill mastery evidence: score to feed BKT, weighted by skill weight */
  masteryEvidence: Array<{
    skillId: string;
    evidenceScore: number;
    weight: number;
  }>;
}

const PASS_THRESHOLD = 0.7;
const AI_CAP = 0.4; // R5: AI ≤ 40% of the total

@Injectable()
export class ProjectScoringService {
  /**
   * Roll up milestone + defence scores into the final project score.
   *
   * Missing components (a milestone not reached, no viva) contribute 0 at
   * their full weight — an incomplete project scores low, it is not
   * graded only on what was done.
   *
   * R5 (AI ≤ 40% of the total) is a **structural weight cap**: the
   * AI-derived components (design + hardening rubric grades + the defence
   * viva) are renormalised to sum to exactly 40% of the scoring weight,
   * and the deterministic components (infra + implementation + final
   * validators) to 60%, before any scores are applied. So the model can
   * never determine more than 40% of the outcome — but an all-perfect
   * project still scores 1.0, because the cap reweights, it doesn't
   * discount.
   */
  rollup(input: ProjectRollupInput): ProjectRollupResult {
    const byKey = new Map(input.milestones.map((m) => [m.key, m]));

    const components: Array<{
      component: string;
      rawScore: number;
      authoredWeight: number;
      aiDerived: boolean;
    }> = [];

    for (const key of [
      'design',
      'infra',
      'implementation',
      'hardening',
      'final',
    ] as const) {
      const m = byKey.get(key);
      components.push({
        component: key,
        rawScore: clamp01(m?.score ?? 0),
        authoredWeight: MILESTONE_WEIGHTS[key],
        aiDerived: Boolean(m?.aiDerived) || AI_COMPONENTS.has(key),
      });
    }
    components.push({
      component: 'defence',
      rawScore: clamp01(input.defenceScore ?? 0),
      authoredWeight: MILESTONE_WEIGHTS.defence,
      aiDerived: true,
    });

    const authoredAi = components
      .filter((c) => c.aiDerived)
      .reduce((s, c) => s + c.authoredWeight, 0);
    const authoredNonAi = components
      .filter((c) => !c.aiDerived)
      .reduce((s, c) => s + c.authoredWeight, 0);

    // Structural R5 cap: AI half → AI_CAP of the weight, deterministic
    // half → (1 - AI_CAP). Renormalise within each half by its authored
    // shares. If either half has no components, the other takes all the
    // weight (no cap to apply).
    const aiCapApplied =
      authoredAi > 0 &&
      authoredNonAi > 0 &&
      authoredAi / (authoredAi + authoredNonAi) > AI_CAP;
    const aiWeightBudget =
      authoredNonAi === 0 ? 1 : authoredAi === 0 ? 0 : AI_CAP;
    const nonAiWeightBudget = 1 - aiWeightBudget;

    const weighted = components.map((c) => {
      const half = c.aiDerived ? authoredAi : authoredNonAi;
      const budget = c.aiDerived ? aiWeightBudget : nonAiWeightBudget;
      const effWeight = half === 0 ? 0 : (c.authoredWeight / half) * budget;
      return { ...c, weight: effWeight, weighted: c.rawScore * effWeight };
    });

    const finalScore = round4(
      clamp01(weighted.reduce((s, c) => s + c.weighted, 0)),
    );
    const finalAiWeighted = weighted
      .filter((c) => c.aiDerived)
      .reduce((s, c) => s + c.weighted, 0);
    const aiFraction =
      finalScore > 0 ? round4(finalAiWeighted / finalScore) : 0;

    // Mastery: every mapped skill gets the final project score as
    // evidence, apportioned by its activity_skill.weight (§2.7 stance,
    // same as the guided-lab path).
    const masteryEvidence = input.mappedSkills.map((s) => ({
      skillId: s.skillId,
      evidenceScore: finalScore,
      weight: s.weight,
    }));

    return {
      finalScore,
      passThreshold: PASS_THRESHOLD,
      passed: finalScore >= PASS_THRESHOLD,
      breakdown: weighted.map((c) => ({
        component: c.component,
        rawScore: round4(c.rawScore),
        weight: round4(c.weight),
        weighted: round4(c.weighted),
        aiDerived: c.aiDerived,
      })),
      aiFraction,
      aiCapApplied,
      masteryEvidence,
    };
  }
}

function clamp01(n: number): number {
  return Math.max(0, Math.min(1, Number.isFinite(n) ? n : 0));
}
function round4(n: number): number {
  return Math.round(n * 1e4) / 1e4;
}
