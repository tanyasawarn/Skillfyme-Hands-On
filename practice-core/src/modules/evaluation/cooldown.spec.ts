import { computeCooldownUntil } from './cooldown';

const HOUR = 60 * 60 * 1000;
const DAY = 24 * HOUR;

describe('computeCooldownUntil (doc §2.7: cooldown = min(24h * 2^(retry_count-1), 7d))', () => {
  const now = new Date('2026-01-01T00:00:00.000Z');

  it('a first-ever failure (retry_index=0, retry_count=1) gets the base 24h cooldown', () => {
    const result = computeCooldownUntil(0, now);
    expect(result.getTime() - now.getTime()).toBe(24 * HOUR);
  });

  it('a second failure (retry_index=1, retry_count=2) gets 48h', () => {
    const result = computeCooldownUntil(1, now);
    expect(result.getTime() - now.getTime()).toBe(48 * HOUR);
  });

  it('a third failure (retry_index=2, retry_count=3) gets 96h', () => {
    const result = computeCooldownUntil(2, now);
    expect(result.getTime() - now.getTime()).toBe(96 * HOUR);
  });

  it('caps at 7 days even for a very high retry count', () => {
    const result = computeCooldownUntil(10, now);
    expect(result.getTime() - now.getTime()).toBe(7 * DAY);
  });

  it('caps exactly at the boundary where 24h * 2^(n-1) would exceed 7d', () => {
    // 2^(retry_count-1) * 24h = 7d at retry_count where 2^(rc-1) = 7 -> rc-1 = log2(7) ≈ 2.807
    // so retry_count=4 (2^3=8 -> 192h=8d) already exceeds 7d and must be capped.
    const result = computeCooldownUntil(3, now); // retry_count = 4
    expect(result.getTime() - now.getTime()).toBe(7 * DAY);
  });
});
