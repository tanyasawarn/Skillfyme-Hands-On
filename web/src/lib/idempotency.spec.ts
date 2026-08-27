import { describe, it, expect, vi, afterEach } from 'vitest';
import { makeIdempotencyKey } from './idempotency';

describe('makeIdempotencyKey (PLAN.md U3)', () => {
  afterEach(() => {
    vi.restoreAllMocks();
  });

  it('matches the original inline template shape: web-<scope>-<timestamp>', () => {
    vi.spyOn(Date, 'now').mockReturnValue(1700000000000);
    expect(makeIdempotencyKey('start-activity-version-abc')).toBe(
      'web-start-activity-version-abc-1700000000000',
    );
  });

  it('two calls with the same scope at different times produce different keys', () => {
    vi.spyOn(Date, 'now').mockReturnValueOnce(1).mockReturnValueOnce(2);
    const first = makeIdempotencyKey('scope');
    const second = makeIdempotencyKey('scope');
    expect(first).not.toBe(second);
  });
});
