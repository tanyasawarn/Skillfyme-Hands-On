import { Logger } from '@nestjs/common';
import type { ConfigService } from '@nestjs/config';
import Anthropic from '@anthropic-ai/sdk';
import Groq from 'groq-sdk';
import { FakeLlmProvider } from './fake-provider';
import type { LlmProvider, ModelTier } from './types';

/**
 * PLAN.md G1 / doc §7.6 -- REAL provider adapters for the LLM Gateway's
 * `complete()` (plain-text generation: Mentor replies, the authoring
 * assistant's draft(), hint RAG, summarisation).
 *
 * This is the sibling of evaluation/llm-tool-call.port.ts's
 * AnthropicToolCaller / GroqToolCaller -- that pair does forced-tool
 * STRUCTURED-JSON calls for the grader + viva; this pair does plain
 * text-completion calls through the gateway (which layers routing,
 * budgeting, caching, failover on top). Same two vendors, same env
 * convention (LLM_PROVIDER / GROQ_API_KEY / ANTHROPIC_API_KEY), same
 * "Groq for dev, Anthropic for prod" stance.
 *
 * Retry + timeout are handled by each SDK (maxRetries + timeout below);
 * failover ACROSS providers is the gateway's job (LlmGatewayService
 * orderByHealth + the per-provider try/catch loop).
 */

// A single-turn completion is fast; the SDK default (10 min) would let
// one hung request stall a mentor reply. 30s is generous for a mid-tier
// text completion.
const REQUEST_TIMEOUT_MS = 30_000;
// SDK-native retry: retryable 429/5xx/network vs non-retryable 4xx, with
// exponential backoff. The gateway's cross-provider failover picks up
// only after these are exhausted.
const MAX_RETRIES = 2;

/** Per-tier model id for each vendor. Overridable via env. */
function anthropicModelForTier(config: ConfigService, tier: ModelTier): string {
  switch (tier) {
    case 'fast':
      return (
        config.get<string>('ANTHROPIC_FAST_MODEL') ?? 'claude-haiku-4-5-20251001'
      );
    case 'mid':
      return config.get<string>('ANTHROPIC_MID_MODEL') ?? 'claude-sonnet-4-5';
    case 'strong':
      return (
        config.get<string>('ANTHROPIC_STRONG_MODEL') ?? 'claude-sonnet-4-5'
      );
  }
}

function groqModelForTier(config: ConfigService, tier: ModelTier): string {
  // Groq's currently broadly-available chat models are the OpenAI
  // gpt-oss pair (the older Llama-3.x ids 404 on newer keys). gpt-oss
  // models are light reasoning models -- they spend some completion
  // budget on hidden reasoning tokens before the visible answer, which
  // the tier output ceilings (router.ts: fast 512 / mid 1024 /
  // strong 2048) leave ample room for. Override per env if a key has
  // access to something better.
  const fast = config.get<string>('GROQ_FAST_MODEL') ?? 'openai/gpt-oss-20b';
  const strong = config.get<string>('GROQ_MODEL') ?? 'openai/gpt-oss-120b';
  return tier === 'fast' ? fast : strong;
}

/** Anthropic Messages API, plain text out. */
export class AnthropicProvider implements LlmProvider {
  readonly name = 'anthropic';
  private readonly logger = new Logger(AnthropicProvider.name);
  private readonly client: Anthropic;
  private readonly modelForTier: (t: ModelTier) => string;

  constructor(apiKey: string, modelForTier: (t: ModelTier) => string) {
    this.client = new Anthropic({
      apiKey,
      timeout: REQUEST_TIMEOUT_MS,
      maxRetries: MAX_RETRIES,
    });
    this.modelForTier = modelForTier;
  }

  async healthy(): Promise<boolean> {
    // No dedicated ping endpoint; a client with a key is assumed live.
    // A real outage surfaces as a call error and the gateway fails over.
    return true;
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
    const model = this.modelForTier(input.tier);
    const msg = await this.client.messages.create({
      model,
      max_tokens: input.maxOutputTokens,
      system: input.system,
      messages: [{ role: 'user', content: input.user }],
    });
    const text = msg.content
      .filter((b): b is Anthropic.TextBlock => b.type === 'text')
      .map((b) => b.text)
      .join('');
    return {
      text,
      model,
      inputTokens: msg.usage?.input_tokens ?? 0,
      outputTokens: msg.usage?.output_tokens ?? 0,
    };
  }
}

/** Groq (OpenAI-compatible chat completions), plain text out. */
export class GroqProvider implements LlmProvider {
  readonly name = 'groq';
  private readonly logger = new Logger(GroqProvider.name);
  private readonly client: Groq;
  private readonly modelForTier: (t: ModelTier) => string;

  constructor(apiKey: string, modelForTier: (t: ModelTier) => string) {
    this.client = new Groq({
      apiKey,
      timeout: REQUEST_TIMEOUT_MS,
      maxRetries: MAX_RETRIES,
    });
    this.modelForTier = modelForTier;
  }

  async healthy(): Promise<boolean> {
    return true;
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
    const model = this.modelForTier(input.tier);
    const completion = await this.client.chat.completions.create({
      model,
      max_tokens: input.maxOutputTokens,
      messages: [
        { role: 'system', content: input.system },
        { role: 'user', content: input.user },
      ],
    });
    const text = completion.choices[0]?.message?.content ?? '';
    return {
      text,
      model,
      inputTokens: completion.usage?.prompt_tokens ?? 0,
      outputTokens: completion.usage?.completion_tokens ?? 0,
    };
  }
}

/**
 * Builds the ordered provider list for the gateway. Task 1's contract:
 *
 *   - development  -> Groq first          (free-tier / fast iteration)
 *   - production   -> Anthropic first     (production default)
 *   - the OTHER real provider is appended as a failover target when its
 *     key is also present.
 *   - FakeLlmProvider is appended LAST as a final safety net ONLY when
 *     no real key is configured at all -- with a real key present it is
 *     never in the list, so "no fake responses when an API key exists"
 *     holds (Task 1 / Task 3 acceptance).
 *
 * Environment resolution order:
 *   1. explicit LLM_PROVIDER=groq|anthropic wins.
 *   2. else NODE_ENV: 'production' -> Anthropic-first, anything else
 *      (development / test / unset) -> Groq-first.
 *   3. within that, a provider is only included if its key is set; if the
 *      preferred one's key is missing, the other real provider leads.
 */
export function buildLlmProviders(config: ConfigService): LlmProvider[] {
  const explicit = (config.get<string>('LLM_PROVIDER') ?? '')
    .trim()
    .toLowerCase();
  const nodeEnv = (
    config.get<string>('NODE_ENV') ??
    process.env.NODE_ENV ??
    'development'
  ).toLowerCase();
  const groqKey = config.get<string>('GROQ_API_KEY');
  const anthropicKey = config.get<string>('ANTHROPIC_API_KEY');

  const groq = groqKey
    ? new GroqProvider(groqKey, (t) => groqModelForTier(config, t))
    : null;
  const anthropic = anthropicKey
    ? new AnthropicProvider(anthropicKey, (t) => anthropicModelForTier(config, t))
    : null;

  // Decide the preferred order.
  let preferAnthropic: boolean;
  if (explicit === 'anthropic') preferAnthropic = true;
  else if (explicit === 'groq') preferAnthropic = false;
  else preferAnthropic = nodeEnv === 'production';

  const ordered: LlmProvider[] = [];
  if (preferAnthropic) {
    if (anthropic) ordered.push(anthropic);
    if (groq) ordered.push(groq);
  } else {
    if (groq) ordered.push(groq);
    if (anthropic) ordered.push(anthropic);
  }

  if (ordered.length === 0) {
    // No real key anywhere -- keep the platform working offline (local
    // dev without keys, CI). This is the ONLY path that yields a fake.
    return [new FakeLlmProvider()];
  }
  return ordered;
}

/** True when at least one real provider key is configured. */
export function hasRealLlmProvider(config: ConfigService): boolean {
  return (
    !!config.get<string>('GROQ_API_KEY') ||
    !!config.get<string>('ANTHROPIC_API_KEY')
  );
}
