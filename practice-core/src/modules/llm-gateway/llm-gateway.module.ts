import { Module } from '@nestjs/common';
import { ConfigService } from '@nestjs/config';
import { BudgetLedger } from './budget-ledger';
import { LlmGatewayService } from './llm-gateway.service';
import { PromptCache } from './prompt-cache';
import { buildLlmProviders } from './providers';
import type { LlmProvider } from './types';

/**
 * PLAN.md G1 / doc §7.6. The gateway is the single chokepoint for every
 * plain-text model call (Mentor replies, the authoring assistant's
 * draft(), hint RAG, summarisation).
 *
 * Providers are assembled by buildLlmProviders (providers.ts):
 *   - development -> GroqProvider first (Groq free-tier, fast iteration)
 *   - production  -> AnthropicProvider first
 *   - the other real provider is appended as a cross-provider failover
 *     target when its key is present
 *   - FakeLlmProvider is used ONLY when NO real key is configured at all
 *     (offline local dev / CI). With a key present the fake never enters
 *     the list -- "no fake responses when an API key exists".
 *
 * Retry + per-request timeout live in each SDK adapter; the gateway
 * layers routing, redaction, prompt cache, budget circuit-breaker, and
 * cross-provider failover on top (LlmGatewayService.call).
 */
@Module({
  providers: [
    {
      provide: 'LLM_PROVIDERS',
      useFactory: (config: ConfigService): LlmProvider[] =>
        buildLlmProviders(config),
      inject: [ConfigService],
    },
    { provide: BudgetLedger, useFactory: () => new BudgetLedger() },
    { provide: PromptCache, useFactory: () => new PromptCache() },
    {
      provide: LlmGatewayService,
      useFactory: (
        providers: LlmProvider[],
        budget: BudgetLedger,
        cache: PromptCache,
      ) => new LlmGatewayService(providers, budget, cache),
      inject: ['LLM_PROVIDERS', BudgetLedger, PromptCache],
    },
  ],
  exports: [LlmGatewayService],
})
export class LlmGatewayModule {}
