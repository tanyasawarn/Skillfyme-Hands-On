import { ProjectScoringService } from './project-scoring';
import type { MilestoneScoreInput } from './project-scoring';

const svc = new ProjectScoringService();

const all = (score: number): MilestoneScoreInput[] => [
  { key: 'design', score },
  { key: 'infra', score },
  { key: 'implementation', score },
  { key: 'hardening', score },
  { key: 'final', score },
];

describe('ProjectScoringService.rollup (Phase 3 3.9 — sp.project.default)', () => {
  it('all-perfect milestones + perfect viva → finalScore 1.0, pass (the R5 cap reweights, it does not discount)', () => {
    const r = svc.rollup({
      milestones: all(1),
      defenceScore: 1,
      mappedSkills: [{ skillId: 's1', weight: 1 }],
    });
    expect(r.finalScore).toBeCloseTo(1, 4);
    expect(r.passed).toBe(true);
    // authored AI weight (design .2 + hardening .2 + defence .15 = .55) > .4
    expect(r.aiCapApplied).toBe(true);
    // …but the AI half only ever accounts for 40% of the weight
    expect(r.aiFraction).toBeCloseTo(0.4, 3);
  });

  it('deterministic half perfect, AI half zero → AI cannot rescue it', () => {
    const ms: MilestoneScoreInput[] = [
      { key: 'design', score: 0 },
      { key: 'infra', score: 1 },
      { key: 'implementation', score: 1 },
      { key: 'hardening', score: 0 },
      { key: 'final', score: 1 },
    ];
    const r = svc.rollup({ milestones: ms, defenceScore: 0, mappedSkills: [] });
    // deterministic half is 60% of the weight, all scored 1 → finalScore 0.6
    expect(r.finalScore).toBeCloseTo(0.6, 3);
    expect(r.passed).toBe(false);
  });

  it('AI half perfect, deterministic half zero → AI capped at 40%', () => {
    const ms: MilestoneScoreInput[] = [
      { key: 'design', score: 1 },
      { key: 'infra', score: 0 },
      { key: 'implementation', score: 0 },
      { key: 'hardening', score: 1 },
      { key: 'final', score: 0 },
    ];
    const r = svc.rollup({ milestones: ms, defenceScore: 1, mappedSkills: [] });
    // AI half is 40% of the weight, all scored 1 → finalScore 0.4
    expect(r.finalScore).toBeCloseTo(0.4, 3);
    expect(r.aiCapApplied).toBe(true);
    expect(r.aiFraction).toBeCloseTo(1, 3); // the whole (small) score is AI
  });

  it('a not-reached milestone contributes 0 at its (reweighted) weight', () => {
    const ms: MilestoneScoreInput[] = [
      { key: 'design', score: 1 },
      { key: 'infra', score: 1 },
      { key: 'implementation', score: 1 },
      { key: 'hardening', score: null }, // not reached (AI component)
      { key: 'final', score: null }, // not reached (deterministic)
    ];
    const r = svc.rollup({ milestones: ms, defenceScore: 1, mappedSkills: [] });
    expect(r.finalScore).toBeLessThan(1);
    expect(r.finalScore).toBeGreaterThan(0.5);
  });

  it('mixed realistic scores land between the halves', () => {
    const ms: MilestoneScoreInput[] = [
      { key: 'design', score: 0.75 },
      { key: 'infra', score: 0.9 },
      { key: 'implementation', score: 0.8 },
      { key: 'hardening', score: 0.6 },
      { key: 'final', score: 0.85 },
    ];
    const r = svc.rollup({
      milestones: ms,
      defenceScore: 0.7,
      mappedSkills: [],
    });
    expect(r.finalScore).toBeGreaterThan(0.7);
    expect(r.finalScore).toBeLessThan(0.9);
    expect(r.passed).toBe(true);
  });

  it('emits per-skill mastery evidence weighted by activity_skill.weight', () => {
    const r = svc.rollup({
      milestones: all(0.8),
      defenceScore: 0.8,
      mappedSkills: [
        { skillId: 's1', weight: 0.7 },
        { skillId: 's2', weight: 0.3 },
      ],
    });
    expect(r.masteryEvidence).toHaveLength(2);
    expect(r.masteryEvidence[0]).toMatchObject({ skillId: 's1', weight: 0.7 });
    expect(r.masteryEvidence[0].evidenceScore).toBeCloseTo(r.finalScore, 6);
  });

  it('breakdown weights sum to 1 and weighted contributions sum to finalScore', () => {
    const r = svc.rollup({
      milestones: all(0.6),
      defenceScore: 0.9,
      mappedSkills: [],
    });
    const weightSum = r.breakdown.reduce((s, c) => s + c.weight, 0);
    const contribSum = r.breakdown.reduce((s, c) => s + c.weighted, 0);
    expect(weightSum).toBeCloseTo(1, 3);
    expect(contribSum).toBeCloseTo(r.finalScore, 3);
  });

  it('AI-weighted portion is never more than 40% of total weight', () => {
    const r = svc.rollup({
      milestones: all(1),
      defenceScore: 1,
      mappedSkills: [],
    });
    const aiWeight = r.breakdown
      .filter((c) => c.aiDerived)
      .reduce((s, c) => s + c.weight, 0);
    expect(aiWeight).toBeLessThanOrEqual(0.4 + 1e-3); // round4 on 3 components
  });
});
