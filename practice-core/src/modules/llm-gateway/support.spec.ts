import { BudgetLedger } from './budget-ledger';
import { PromptCache } from './prompt-cache';
import { redact } from './redactor';
import { estimateCostUsd, maxOutputTokensFor, tierFor } from './router';
import { BudgetExhaustedError } from './types';

describe('router (G1)', () => {
  it('maps task classes to tiers per §7.6', () => {
    expect(tierFor('intent_classify')).toBe('fast');
    expect(tierFor('mentor_reply')).toBe('mid');
    expect(tierFor('grade')).toBe('strong');
  });
  it('clamps requested output tokens to the tier ceiling', () => {
    expect(maxOutputTokensFor('fast', 99999)).toBe(512);
    expect(maxOutputTokensFor('strong', 100)).toBe(100);
    expect(maxOutputTokensFor('mid')).toBe(1024);
  });
  it('strong tier costs more than fast for the same token counts', () => {
    expect(estimateCostUsd('strong', 1000, 1000)).toBeGreaterThan(
      estimateCostUsd('fast', 1000, 1000),
    );
  });
});

describe('PromptCache (G1)', () => {
  it('exact hit on identical inputs', () => {
    const c = new PromptCache();
    c.put('v1', 'sys', 'user q', {
      text: 'a',
      model: 'm',
      inputTokens: 1,
      outputTokens: 1,
    });
    const h = c.get('v1', 'sys', 'user q');
    expect(h?.kind).toBe('exact');
    expect(h?.text).toBe('a');
  });
  it('miss on a different prompt version', () => {
    const c = new PromptCache();
    c.put('v1', 'sys', 'q', {
      text: 'a',
      model: 'm',
      inputTokens: 1,
      outputTokens: 1,
    });
    expect(c.get('v2', 'sys', 'q')).toBeNull();
  });
  it('semantic hit on a reworded question, miss on an unrelated one', () => {
    const c = new PromptCache();
    c.put(
      'v1',
      'sys',
      'how does a kubernetes readiness probe work and what does it affect',
      { text: 'answer', model: 'm', inputTokens: 1, outputTokens: 1 },
    );
    expect(
      c.get(
        'v1',
        'sys',
        'how does a kubernetes readiness probe work, and what does it affect?',
      )?.kind,
    ).toBe('semantic');
    expect(c.get('v1', 'sys', 'what is a terraform backend')).toBeNull();
  });
  it('evicts expired entries', () => {
    const c = new PromptCache(1 /* ttl 1ms */);
    c.put('v1', 's', 'q', {
      text: 'a',
      model: 'm',
      inputTokens: 1,
      outputTokens: 1,
    });
    return new Promise((r) => setTimeout(r, 5)).then(() => {
      expect(c.get('v1', 's', 'q')).toBeNull();
    });
  });
});

describe('BudgetLedger (G1)', () => {
  it('throws at the attempt scope when a call would breach it', () => {
    const b = new BudgetLedger({ perAttemptUsd: 0.01 });
    b.charge(0.009, { attemptId: 'a' });
    expect(() => b.checkOrThrow(0.005, { attemptId: 'a' })).toThrow(
      BudgetExhaustedError,
    );
  });
  it('separate attempts have separate budgets', () => {
    const b = new BudgetLedger({ perAttemptUsd: 0.01 });
    b.charge(0.009, { attemptId: 'a' });
    expect(() => b.checkOrThrow(0.005, { attemptId: 'b' })).not.toThrow();
  });
  it('opens the global breaker after 5 consecutive provider errors', () => {
    const b = new BudgetLedger();
    for (let i = 0; i < 5; i++) b.noteProviderError();
    expect(b.breakerOpen).toBe(true);
    expect(() => b.checkOrThrow(0, {})).toThrow(BudgetExhaustedError);
  });
  it('a successful charge resets the consecutive-error counter', () => {
    const b = new BudgetLedger();
    b.noteProviderError();
    b.noteProviderError();
    b.charge(0.001, {});
    b.noteProviderError();
    expect(b.breakerOpen).toBe(false);
  });
});

describe('redact (G1 / §7.6)', () => {
  it('strips AWS keys, api keys, jwts, private keys, emails', () => {
    const { text, hits } = redact(
      [
        'AKIAIOSFODNN7EXAMPLE',
        'sk-abcdefghijklmnop0123456789',
        'eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.SflKxwRJSMeKKF2QT4fwpMeJf36POk',
        'contact me at learner@example.com',
      ].join('\n'),
    );
    expect(text).not.toContain('AKIAIOSFODNN7EXAMPLE');
    expect(text).not.toContain('learner@example.com');
    expect(hits).toEqual(
      expect.arrayContaining(['aws_access_key_id', 'api_key', 'jwt', 'email']),
    );
  });
  it('leaves clean text untouched', () => {
    const { text, hits } = redact('A readiness probe gates traffic to a pod.');
    expect(text).toBe('A readiness probe gates traffic to a pod.');
    expect(hits).toEqual([]);
  });
});
