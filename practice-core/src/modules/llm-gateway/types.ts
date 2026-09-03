/**
 * PLAN.md G1 / doc §7.6 -- the LLM Gateway: a single chokepoint for
 * every model call in the platform (mentor, grader, authoring assistant,
 * feedback, summarisation).
 */

/** Task class -> model tier (doc §7.6 Routing row). */
export type TaskClass =
  | 'intent_classify' // cheap/fast
  | 'hint_rag' // cheap/fast
  | 'summarise' // cheap/fast
  | 'mentor_reply' // mid
  | 'feedback' // mid
  | 'grade' // strong
  | 'architecture_review' // strong
  | 'viva' // strong
  | 'authoring'; // strong

export type ModelTier = 'fast' | 'mid' | 'strong';

export interface LlmRequest {
  taskClass: TaskClass;
  /** The prompt-registry id (G7). No inline prompt strings (doc §7.6). */
  promptVersion: string;
  /** Fully-rendered messages -- system prompt already interpolated. */
  system: string;
  user: string;
  /** Scope ids for budgeting + observability. */
  attemptId?: string;
  userId?: string;
  tenantId?: string;
  /** Hard cap for this call; the gateway also enforces scope budgets. */
  maxOutputTokens?: number;
  /** Skip the cache for this call (e.g. a retry after a bad answer). */
  noCache?: boolean;
}

export interface LlmResponse {
  text: string;
  model: string;
  tier: ModelTier;
  promptVersion: string;
  inputTokens: number;
  outputTokens: number;
  costUsd: number;
  latencyMs: number;
  cacheHit: 'none' | 'exact' | 'semantic';
  provider: string;
  /** Set when the gateway degraded instead of calling a model. */
  degraded?: 'budget_exhausted' | 'all_providers_down';
}

export interface LlmProvider {
  readonly name: string;
  /** Cheap liveness probe -- doc §7.6 "health-checked, automatic failover". */
  healthy(): Promise<boolean>;
  complete(input: {
    tier: ModelTier;
    system: string;
    user: string;
    maxOutputTokens: number;
  }): Promise<{
    text: string;
    model: string;
    inputTokens: number;
    outputTokens: number;
  }>;
}

export class BudgetExhaustedError extends Error {
  constructor(public readonly scope: 'attempt' | 'user' | 'tenant' | 'global') {
    super(`LLM budget exhausted at ${scope} scope`);
    this.name = 'BudgetExhaustedError';
  }
}
