import { BudgetLedger } from './budget-ledger';
import { FakeLlmProvider, SecondaryFakeLlmProvider } from './fake-provider';
import { LlmGatewayService, type LlmCallRecord } from './llm-gateway.service';
import { PromptCache } from './prompt-cache';
import type { LlmRequest } from './types';

const baseReq: LlmRequest = {
  taskClass: 'mentor_reply',
  promptVersion: 'mentor.system.v1',
  system: 'You are a mentor. Disclosure ceiling 1.',
  user: 'How does a readiness probe work?',
  attemptId: 'att-1',
  userId: 'usr-1',
  tenantId: 'ten-1',
};

describe('LlmGatewayService (PLAN.md G1 / doc §7.6)', () => {
  it('routes a fast task to the fast tier and a strong task to strong', async () => {
    const p = new FakeLlmProvider();
    const gw = new LlmGatewayService([p]);
    const fast = await gw.call({
      ...baseReq,
      taskClass: 'intent_classify',
      attemptId: 'r-fast',
      user: 'classify: concept question',
      noCache: true,
    });
    const strong = await gw.call({
      ...baseReq,
      taskClass: 'grade',
      attemptId: 'r-strong',
      user: 'grade this incident note against rub.incident-note.v2',
      maxOutputTokens: 300,
      noCache: true,
    });
    expect(fast.tier).toBe('fast');
    expect(strong.tier).toBe('strong');
    expect(fast.costUsd).toBeGreaterThan(0);
    expect(strong.costUsd).toBeGreaterThan(fast.costUsd);
  });

  it('exact-cache: an identical second call is a cache hit with zero cost', async () => {
    const p = new FakeLlmProvider();
    const gw = new LlmGatewayService([p]);
    const first = await gw.call(baseReq);
    const second = await gw.call(baseReq);
    expect(first.cacheHit).toBe('none');
    expect(second.cacheHit).toBe('exact');
    expect(second.costUsd).toBe(0);
    expect(p.calls).toBe(1); // provider hit once
  });

  it('semantic-cache: a near-identical question hits the cache', async () => {
    const p = new FakeLlmProvider();
    const gw = new LlmGatewayService([p]);
    await gw.call({
      ...baseReq,
      user: 'Explain how a Kubernetes readiness probe works and what it affects.',
    });
    const near = await gw.call({
      ...baseReq,
      user: 'Explain how a kubernetes readiness probe works, and what does it affect?',
    });
    expect(near.cacheHit).toBe('semantic');
    expect(p.calls).toBe(1);
  });

  it('redaction: an API key in context is stripped before it reaches the provider', async () => {
    const p = new FakeLlmProvider();
    const records: LlmCallRecord[] = [];
    const gw = new LlmGatewayService(
      [p],
      new BudgetLedger(),
      new PromptCache(),
      (r) => records.push(r),
    );
    // spy on what the provider sees
    let seenUser = '';
    const origComplete = p.complete.bind(p);
    p.complete = async (i) => {
      seenUser = i.user;
      return origComplete(i);
    };
    await gw.call({
      ...baseReq,
      noCache: true,
      user: 'my key is sk-abcdef0123456789abcdef and it fails',
    });
    expect(seenUser).not.toContain('sk-abcdef0123456789abcdef');
    expect(seenUser).toContain('[redacted:api_key]');
    expect(records[0].redactionHits).toContain('api_key');
  });

  it('failover: an unhealthy primary provider is skipped for the secondary', async () => {
    const primary = new FakeLlmProvider();
    primary.setHealthy(false);
    const secondary = new SecondaryFakeLlmProvider();
    const gw = new LlmGatewayService([primary, secondary]);
    const res = await gw.call({ ...baseReq, noCache: true });
    expect(res.provider).toBe('fake-secondary');
    expect(res.degraded).toBeUndefined();
  });

  it('failover: a throwing primary is retried on the secondary', async () => {
    const primary = new FakeLlmProvider();
    primary.complete = async () => {
      throw new Error('500 from provider');
    };
    const secondary = new SecondaryFakeLlmProvider();
    const gw = new LlmGatewayService([primary, secondary]);
    const res = await gw.call({ ...baseReq, noCache: true });
    expect(res.provider).toBe('fake-secondary');
  });

  it('degrades (not throws) when every provider is down — mentor down must not block an attempt', async () => {
    const p = new FakeLlmProvider();
    p.complete = async () => {
      throw new Error('down');
    };
    const gw = new LlmGatewayService([p]);
    const res = await gw.call({ ...baseReq, noCache: true });
    expect(res.degraded).toBe('all_providers_down');
    expect(res.text).toBe('');
  });

  it('degrades on per-attempt budget exhaustion', async () => {
    const p = new FakeLlmProvider();
    const budget = new BudgetLedger({ perAttemptUsd: 0.0001 }); // tiny
    const gw = new LlmGatewayService([p], budget);
    const res = await gw.call({
      ...baseReq,
      taskClass: 'grade',
      noCache: true,
    });
    expect(res.degraded).toBe('budget_exhausted');
    expect(p.calls).toBe(0); // never called the model
  });

  it('charges the ledger on a real call and reports spend per attempt', async () => {
    const p = new FakeLlmProvider();
    const budget = new BudgetLedger();
    const gw = new LlmGatewayService([p], budget);
    await gw.call({ ...baseReq, noCache: true });
    expect(budget.spentOnAttempt('att-1')).toBeGreaterThan(0);
  });

  it('observability: every call is recorded with prompt version, model, tokens, cost, attempt id', async () => {
    const p = new FakeLlmProvider();
    const records: LlmCallRecord[] = [];
    const gw = new LlmGatewayService(
      [p],
      new BudgetLedger(),
      new PromptCache(),
      (r) => records.push(r),
    );
    await gw.call({ ...baseReq, noCache: true });
    expect(records).toHaveLength(1);
    expect(records[0]).toMatchObject({
      promptVersion: 'mentor.system.v1',
      taskClass: 'mentor_reply',
      attemptId: 'att-1',
      cacheHit: 'none',
    });
    expect(records[0].costUsd).toBeGreaterThan(0);
  });

  it('circuit breaker: 5 consecutive provider errors open the global breaker', async () => {
    const p = new FakeLlmProvider();
    p.complete = async () => {
      throw new Error('boom');
    };
    const budget = new BudgetLedger();
    const gw = new LlmGatewayService([p], budget);
    for (let i = 0; i < 5; i++) {
      await gw.call({ ...baseReq, attemptId: `a${i}`, noCache: true });
    }
    expect(budget.breakerOpen).toBe(true);
    const res = await gw.call({
      ...baseReq,
      attemptId: 'a-after',
      noCache: true,
    });
    expect(res.degraded).toBe('budget_exhausted'); // global breaker path
  });
});
