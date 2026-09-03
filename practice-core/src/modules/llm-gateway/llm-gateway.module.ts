import { Module } from '@nestjs/common';
import { ConfigService } from '@nestjs/config';
import { BudgetLedger } from './budget-ledger';
import { FakeLlmProvider } from './fake-provider';
import { LlmGatewayService } from './llm-gateway.service';
import { PromptCache } from './prompt-cache';
import type { LlmProvider } from './types';

/**
 * PLAN.md G1 / doc §7.6. The gateway is the single chokepoint for every
 * model call. Providers are assembled by a factory: a real deployment
 * lists (anthropic, openai, ...) health-checked in priority order; here
 * we ship the FakeLlmProvider so every downstream feature (Mentor, and
 * later grader/authoring routed through the gateway) works offline. A
 * real provider list is a drop-in behind the LlmProvider interface.
 */
@Module({
  providers: [
    {
      provide: 'LLM_PROVIDERS',
      useFactory: (_config: ConfigService): LlmProvider[] => {
        // TODO(real deployment): build from ANTHROPIC_API_KEY / OPENAI_API_KEY,
        // ordered by priority, each wrapped in an LlmProvider adapter.
        return [new FakeLlmProvider()];
      },
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
