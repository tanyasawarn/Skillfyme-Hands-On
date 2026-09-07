import { Logger } from '@nestjs/common';
import type { ConfigService } from '@nestjs/config';
import Anthropic from '@anthropic-ai/sdk';
import Groq from 'groq-sdk';

/**
 * Provider-neutral "force one tool, get its arguments back as JSON" call.
 *
 * Doc §6.5 rule 32 (structured output only) and Phase 3 3.8's viva
 * generator both want exactly one thing from the model: a single,
 * schema-shaped JSON object, never free text. Anthropic expresses that as
 * forced tool_choice on one tool; Groq (OpenAI-compatible) expresses the
 * same thing as `tool_choice: {type:'function', function:{name}}`. This
 * port is the seam between "how we ask" and "which vendor answers", so
 * ClaudeAiGrader / RealVivaModel keep their prompt-construction and
 * validation logic unchanged regardless of provider.
 *
 * PLAN.md §7.6's LLM Gateway (multi-provider failover, budget circuit
 * breaker, caching) is Phase 4 and lands as its own service; this is the
 * pre-Phase-4 direct-call stance (same as the Phase-2 grader and the
 * Phase-3 viva model), just no longer hard-wired to a single vendor.
 * Groq is the testing/free-tier provider; Anthropic is the production
 * default. Neither is free for lifetime -- both meter per token.
 */

export const LLM_TOOL_CALLER = Symbol('LLM_TOOL_CALLER');

/** A JSON-Schema object describing the single tool's input. */
export interface LlmToolSchema {
  name: string;
  description: string;
  /** JSON Schema (draft 2020-12 subset both providers accept). */
  inputSchema: Record<string, unknown>;
}

export interface LlmToolCallRequest {
  system: string;
  user: string;
  tool: LlmToolSchema;
  maxTokens: number;
  /**
   * Marks the system prompt + tool schema as cache-eligible where the
   * provider supports it (Anthropic ephemeral cache). Ignored by
   * providers without a prompt cache.
   */
  cacheable?: boolean;
}

export interface LlmToolCaller {
  /**
   * Sends the prompt with exactly one tool, forces the model to call it,
   * and returns the parsed tool arguments. Throws if the model does not
   * emit a tool call or the arguments are not valid JSON -- callers do
   * their own value-level validation on the returned object.
   */
  callTool(req: LlmToolCallRequest): Promise<unknown>;
}

// Shared bounds. A single-turn forced-tool completion is fast; the SDK
// default (10 min) is far too generous and would let one hung request
// stall a whole evaluate() pipeline. 45s is generous for the completion
// itself and bounds worst-case multi-sample latency to a few minutes.
const REQUEST_TIMEOUT_MS = 45_000;
// SDK-native retry (retryable 429/5xx/network vs non-retryable 4xx,
// exponential backoff) rather than a hand-rolled loop.
const MAX_RETRIES = 2;

/** Anthropic Messages API, forced tool_choice on one tool. */
export class AnthropicToolCaller implements LlmToolCaller {
  private readonly logger = new Logger(AnthropicToolCaller.name);
  private readonly client: Anthropic;
  private readonly model: string;

  constructor(apiKey: string, model: string) {
    this.client = new Anthropic({
      apiKey,
      timeout: REQUEST_TIMEOUT_MS,
      maxRetries: MAX_RETRIES,
    });
    this.model = model;
  }

  async callTool(req: LlmToolCallRequest): Promise<unknown> {
    const message = await this.client.messages.create({
      model: this.model,
      max_tokens: req.maxTokens,
      system: req.cacheable
        ? [
            {
              type: 'text',
              text: req.system,
              cache_control: { type: 'ephemeral' },
            },
          ]
        : req.system,
      messages: [{ role: 'user', content: req.user }],
      tools: [
        {
          name: req.tool.name,
          description: req.tool.description,
          input_schema: req.tool
            .inputSchema as Anthropic.Tool.InputSchema,
          ...(req.cacheable
            ? { cache_control: { type: 'ephemeral' as const } }
            : {}),
        },
      ],
      tool_choice: { type: 'tool', name: req.tool.name },
    });

    const toolUse = message.content.find((b) => b.type === 'tool_use');
    if (!toolUse || toolUse.type !== 'tool_use') {
      throw new Error(
        `AnthropicToolCaller: expected a tool_use block for tool=${req.tool.name}, got: ${JSON.stringify(message.content)}`,
      );
    }
    // Anthropic returns already-parsed input; normalise through JSON so
    // both provider paths hand callers the same thing.
    return JSON.parse(JSON.stringify(toolUse.input));
  }
}

/**
 * Groq (OpenAI-compatible chat completions), tool_choice forcing the one
 * function. Used for testing on Groq's free tier before switching to
 * Anthropic in production -- GROQ_API_KEY + LLM_PROVIDER=groq.
 *
 * Groq's free tier is rate-limited (requests/min, tokens/min, tokens/day)
 * and meant for evaluation, not sustained grading traffic; expect 429s
 * under load, which the SDK ret/ry handles up to MAX_RETRIES.
 */
export class GroqToolCaller implements LlmToolCaller {
  private readonly logger = new Logger(GroqToolCaller.name);
  private readonly client: Groq;
  private readonly model: string;

  constructor(apiKey: string, model: string) {
    this.client = new Groq({
      apiKey,
      timeout: REQUEST_TIMEOUT_MS,
      maxRetries: MAX_RETRIES,
    });
    this.model = model;
  }

  async callTool(req: LlmToolCallRequest): Promise<unknown> {
    // Groq has no prompt cache; req.cacheable is intentionally ignored.
    const completion = await this.client.chat.completions.create({
      model: this.model,
      max_tokens: req.maxTokens,
      messages: [
        { role: 'system', content: req.system },
        { role: 'user', content: req.user },
      ],
      tools: [
        {
          type: 'function',
          function: {
            name: req.tool.name,
            description: req.tool.description,
            parameters: req.tool.inputSchema,
          },
        },
      ],
      tool_choice: {
        type: 'function',
        function: { name: req.tool.name },
      },
    });

    const call = completion.choices[0]?.message?.tool_calls?.[0];
    if (!call || call.function?.name !== req.tool.name) {
      throw new Error(
        `GroqToolCaller: expected a tool call to ${req.tool.name}, got: ${JSON.stringify(completion.choices[0]?.message)}`,
      );
    }
    try {
      return JSON.parse(call.function.arguments);
    } catch (e) {
      throw new Error(
        `GroqToolCaller: tool arguments for ${req.tool.name} were not valid JSON: ${call.function.arguments}`,
      );
    }
  }
}

/**
 * Selects the tool-caller from config, or returns null when no provider
 * key is set (callers then fall back to their Fake* implementation, same
 * rule the module factories already used for ANTHROPIC_API_KEY alone).
 *
 * LLM_PROVIDER: 'groq' | 'anthropic' | unset.
 *  - explicit 'groq'      -> GroqToolCaller      (needs GROQ_API_KEY)
 *  - explicit 'anthropic' -> AnthropicToolCaller (needs ANTHROPIC_API_KEY)
 *  - unset: infer from whichever key is present; ANTHROPIC_API_KEY wins
 *           if both are, preserving the prior default.
 */
export function createLlmToolCaller(
  config: ConfigService,
): LlmToolCaller | null {
  const provider = (
    config.get<string>('LLM_PROVIDER') ?? ''
  ).toLowerCase();
  const groqKey = config.get<string>('GROQ_API_KEY');
  const anthropicKey = config.get<string>('ANTHROPIC_API_KEY');

  const wantGroq =
    provider === 'groq' || (provider === '' && !anthropicKey && !!groqKey);
  const wantAnthropic =
    provider === 'anthropic' || (provider === '' && !!anthropicKey);

  if (wantGroq) {
    if (!groqKey) return null;
    const model =
      config.get<string>('GROQ_MODEL') ?? 'llama-3.3-70b-versatile';
    return new GroqToolCaller(groqKey, model);
  }
  if (wantAnthropic) {
    if (!anthropicKey) return null;
    const model =
      config.get<string>('ANTHROPIC_GRADER_MODEL') ?? 'claude-sonnet-4-5';
    return new AnthropicToolCaller(anthropicKey, model);
  }
  return null;
}

/**
 * Same selection, but for the viva generator, which has its own model
 * override env (ANTHROPIC_VIVA_MODEL) while sharing GROQ_MODEL.
 */
export function createVivaLlmToolCaller(
  config: ConfigService,
): LlmToolCaller | null {
  const provider = (
    config.get<string>('LLM_PROVIDER') ?? ''
  ).toLowerCase();
  const groqKey = config.get<string>('GROQ_API_KEY');
  const anthropicKey = config.get<string>('ANTHROPIC_API_KEY');

  const wantGroq =
    provider === 'groq' || (provider === '' && !anthropicKey && !!groqKey);
  const wantAnthropic =
    provider === 'anthropic' || (provider === '' && !!anthropicKey);

  if (wantGroq) {
    if (!groqKey) return null;
    const model =
      config.get<string>('GROQ_MODEL') ?? 'llama-3.3-70b-versatile';
    return new GroqToolCaller(groqKey, model);
  }
  if (wantAnthropic) {
    if (!anthropicKey) return null;
    const model =
      config.get<string>('ANTHROPIC_VIVA_MODEL') ?? 'claude-sonnet-4-5';
    return new AnthropicToolCaller(anthropicKey, model);
  }
  return null;
}
