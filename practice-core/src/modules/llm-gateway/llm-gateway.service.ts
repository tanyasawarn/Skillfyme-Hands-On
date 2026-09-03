import { Injectable, Logger } from '@nestjs/common';
import { BudgetLedger } from './budget-ledger';
import { PromptCache } from './prompt-cache';
import { redact } from './redactor';
import {
  estimateCostUsd,
  estimateTokens,
  maxOutputTokensFor,
  tierFor,
} from './router';
import {
  BudgetExhaustedError,
  type LlmProvider,
  type LlmRequest,
  type LlmResponse,
} from './types';

/**
 * PLAN.md G1 / doc §7.6 -- the single chokepoint for every model call.
 *
 * call() pipeline:
 *   1. route: taskClass -> model tier, clamp maxOutputTokens
 *   2. redact: strip credentials / PII from system+user before egress
 *   3. cache: exact then semantic on (promptVersion, context_hash)
 *   4. budget: pre-check per-attempt/user/tenant/global + circuit breaker
 *   5. provider: first healthy provider; failover on error
 *   6. account: charge the ledger, record the call for observability
 *   7. degrade: on BudgetExhaustedError or all-providers-down, return a
 *      degraded response the caller falls back on (never throw upward --
 *      "the mentor being down must not block an attempt").
 */
@Injectable()
export class LlmGatewayService {
  private readonly logger = new Logger(LlmGatewayService.name);

  constructor(
    private readonly providers: LlmProvider[],
    private readonly budget: BudgetLedger = new BudgetLedger(),
    private readonly cache: PromptCache = new PromptCache(),
    private readonly observer: (rec: LlmCallRecord) => void = () => {},
  ) {
    if (providers.length === 0) {
      throw new Error('LlmGatewayService needs at least one provider');
    }
  }

  async call(req: LlmRequest): Promise<LlmResponse> {
    const start = Date.now();
    const tier = tierFor(req.taskClass);
    const maxOut = maxOutputTokensFor(tier, req.maxOutputTokens);

    // 2. redact
    const redSystem = redact(req.system);
    const redUser = redact(req.user);
    const system = redSystem.text;
    const user = redUser.text;
    const redactionHits = Array.from(
      new Set([...redSystem.hits, ...redUser.hits]),
    );

    // 3. cache
    if (!req.noCache) {
      const hit = this.cache.get(req.promptVersion, system, user);
      if (hit) {
        const costUsd = 0;
        const res: LlmResponse = {
          text: hit.text,
          model: hit.model,
          tier,
          promptVersion: req.promptVersion,
          inputTokens: hit.inputTokens,
          outputTokens: hit.outputTokens,
          costUsd,
          latencyMs: Date.now() - start,
          cacheHit: hit.kind,
          provider: 'cache',
        };
        this.record(req, res, redactionHits);
        return res;
      }
    }

    // 4. budget pre-check
    const estCost = estimateCostUsd(
      tier,
      estimateTokens(system) + estimateTokens(user),
      maxOut,
    );
    try {
      this.budget.checkOrThrow(estCost, req);
    } catch (e) {
      if (e instanceof BudgetExhaustedError) {
        return this.degraded(req, tier, start, 'budget_exhausted', e.scope);
      }
      throw e;
    }

    // 5. provider with failover
    const ordered = await this.orderByHealth();
    let lastErr: unknown;
    for (const provider of ordered) {
      try {
        const out = await provider.complete({
          tier,
          system,
          user,
          maxOutputTokens: maxOut,
        });
        const costUsd = estimateCostUsd(
          tier,
          out.inputTokens,
          out.outputTokens,
        );
        this.budget.charge(costUsd, req);
        this.cache.put(req.promptVersion, system, user, {
          text: out.text,
          model: out.model,
          inputTokens: out.inputTokens,
          outputTokens: out.outputTokens,
        });
        const res: LlmResponse = {
          text: out.text,
          model: out.model,
          tier,
          promptVersion: req.promptVersion,
          inputTokens: out.inputTokens,
          outputTokens: out.outputTokens,
          costUsd,
          latencyMs: Date.now() - start,
          cacheHit: 'none',
          provider: provider.name,
        };
        this.record(req, res, redactionHits);
        return res;
      } catch (err) {
        lastErr = err;
        this.budget.noteProviderError();
        this.logger.warn(
          `provider ${provider.name} failed (${String(err)}), trying next`,
        );
      }
    }

    this.logger.error(`all providers failed: ${String(lastErr)}`);
    return this.degraded(req, tier, start, 'all_providers_down');
  }

  private async orderByHealth(): Promise<LlmProvider[]> {
    const withHealth = await Promise.all(
      this.providers.map(async (p) => ({
        p,
        healthy: await p.healthy().catch(() => false),
      })),
    );
    // healthy first, original order preserved within each group
    return [
      ...withHealth.filter((x) => x.healthy).map((x) => x.p),
      ...withHealth.filter((x) => !x.healthy).map((x) => x.p),
    ];
  }

  private degraded(
    req: LlmRequest,
    tier: LlmResponse['tier'],
    start: number,
    reason: NonNullable<LlmResponse['degraded']>,
    scope?: string,
  ): LlmResponse {
    const res: LlmResponse = {
      text: '',
      model: 'none',
      tier,
      promptVersion: req.promptVersion,
      inputTokens: 0,
      outputTokens: 0,
      costUsd: 0,
      latencyMs: Date.now() - start,
      cacheHit: 'none',
      provider: 'none',
      degraded: reason,
    };
    this.logger.warn(
      `degraded (${reason}${scope ? `:${scope}` : ''}) for task=${req.taskClass} attempt=${req.attemptId ?? '-'}`,
    );
    this.record(req, res, []);
    return res;
  }

  private record(
    req: LlmRequest,
    res: LlmResponse,
    redactionHits: string[],
  ): void {
    // doc §7.6 Observability: every call logged with prompt version,
    // model, tokens, latency, cost, attempt id, cache + degrade verdict.
    this.observer({
      taskClass: req.taskClass,
      promptVersion: res.promptVersion,
      model: res.model,
      provider: res.provider,
      inputTokens: res.inputTokens,
      outputTokens: res.outputTokens,
      costUsd: res.costUsd,
      latencyMs: res.latencyMs,
      cacheHit: res.cacheHit,
      degraded: res.degraded ?? null,
      redactionHits,
      attemptId: req.attemptId ?? null,
      userId: req.userId ?? null,
      tenantId: req.tenantId ?? null,
    });
  }
}

export interface LlmCallRecord {
  taskClass: string;
  promptVersion: string;
  model: string;
  provider: string;
  inputTokens: number;
  outputTokens: number;
  costUsd: number;
  latencyMs: number;
  cacheHit: 'none' | 'exact' | 'semantic';
  degraded: string | null;
  redactionHits: string[];
  attemptId: string | null;
  userId: string | null;
  tenantId: string | null;
}
