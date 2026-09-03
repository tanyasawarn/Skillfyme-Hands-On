import type { LlmProvider, ModelTier } from './types';

/**
 * Deterministic in-process provider for tests and for any environment
 * without a real API key. Echoes a bounded, shaped answer so the
 * gateway's routing / caching / budgeting / failover logic is fully
 * exercisable offline (mirrors FakeAiGrader's role for the grader).
 */
export class FakeLlmProvider implements LlmProvider {
  readonly name: string = 'fake';
  private up = true;
  private callCount = 0;

  /** Test hook: flip health. */
  setHealthy(v: boolean) {
    this.up = v;
  }
  get calls() {
    return this.callCount;
  }

  async healthy(): Promise<boolean> {
    return this.up;
  }

  async complete(input: {
    tier: ModelTier;
    system: string;
    user: string;
    maxOutputTokens: number;
  }): Promise<{
    text: string;
    model: string;
    inputTokens: number;
    outputTokens: number;
  }> {
    this.callCount++;
    if (!this.up) throw new Error('fake provider is down');
    const text =
      `[${input.tier}] Considering your question: "${input.user.slice(0, 80)}" — ` +
      `here is a concept-level explanation (no commands, no solution).`;
    return {
      text: text.slice(0, input.maxOutputTokens * 4),
      model: `fake-${input.tier}`,
      inputTokens: Math.ceil((input.system.length + input.user.length) / 4),
      outputTokens: Math.ceil(text.length / 4),
    };
  }
}

/** A second fake, used to prove failover picks up when the first is down. */
export class SecondaryFakeLlmProvider extends FakeLlmProvider {
  readonly name = 'fake-secondary';
  constructor() {
    super();
  }
}
