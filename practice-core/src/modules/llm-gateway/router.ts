import type { ModelTier, TaskClass } from './types';

/**
 * Doc §7.6 Routing: "Task-class -> model tier. Cheap fast model for
 * intent classification, hint RAG, summarisation. Strong model for
 * grading, architecture review, viva, disagreement escalation. Route by
 * policy, not hardcoded call sites."
 */
const TASK_TIER: Record<TaskClass, ModelTier> = {
  intent_classify: 'fast',
  hint_rag: 'fast',
  summarise: 'fast',
  mentor_reply: 'mid',
  feedback: 'mid',
  grade: 'strong',
  architecture_review: 'strong',
  viva: 'strong',
  authoring: 'strong',
};

// Per-tier default output-token ceiling (a call may request less).
const TIER_MAX_OUTPUT: Record<ModelTier, number> = {
  fast: 512,
  mid: 1024,
  strong: 2048,
};

// Rough blended $/1K tokens per tier -- used for pre-call budget checks
// and cost accounting. Real per-model rates come from the provider.
export const TIER_RATE_USD_PER_1K: Record<
  ModelTier,
  { input: number; output: number }
> = {
  fast: { input: 0.0008, output: 0.004 },
  mid: { input: 0.003, output: 0.015 },
  strong: { input: 0.015, output: 0.075 },
};

export function tierFor(taskClass: TaskClass): ModelTier {
  return TASK_TIER[taskClass];
}

export function maxOutputTokensFor(
  tier: ModelTier,
  requested?: number,
): number {
  const ceiling = TIER_MAX_OUTPUT[tier];
  if (requested == null || requested <= 0) return ceiling;
  return Math.min(requested, ceiling);
}

export function estimateCostUsd(
  tier: ModelTier,
  inputTokens: number,
  outputTokens: number,
): number {
  const r = TIER_RATE_USD_PER_1K[tier];
  return (inputTokens / 1000) * r.input + (outputTokens / 1000) * r.output;
}

/** Cheap token estimate (~4 chars/token) for pre-call budgeting. */
export function estimateTokens(text: string): number {
  return Math.ceil(text.length / 4);
}
