import { describe, it, expect } from 'vitest';
import { formatPercent, formatCurrency, formatMode } from './format';

describe('formatPercent', () => {
  it('formats a 0-1 fraction as a rounded whole-number percentage', () => {
    expect(formatPercent(0.5)).toBe('50%');
    expect(formatPercent(1)).toBe('100%');
    expect(formatPercent(0)).toBe('0%');
  });

  it('rounds to the nearest whole percent', () => {
    expect(formatPercent(0.495)).toBe('50%'); // 49.5 -> 50
    expect(formatPercent(0.001)).toBe('0%'); // 0.1 -> 0
  });

  it('never renders "-0%" for a value that rounds to negative zero', () => {
    expect(formatPercent(-0.001)).toBe('0%');
    expect(formatPercent(-0)).toBe('0%');
  });
});

describe('formatCurrency', () => {
  it('formats a number as a dollar amount with 2 decimal places', () => {
    expect(formatCurrency(0.02)).toBe('$0.02');
    expect(formatCurrency(1.5)).toBe('$1.50');
    expect(formatCurrency(10)).toBe('$10.00');
  });
});

describe('formatMode', () => {
  it('replaces underscores with spaces for real mode values', () => {
    expect(formatMode('GUIDED_LAB')).toBe('GUIDED LAB');
    expect(formatMode('PRODUCTION_SIM')).toBe('PRODUCTION SIM');
    expect(formatMode('PROJECT')).toBe('PROJECT');
  });

  it('replaces every underscore, not just the first (the real U2 bug: a single-replace call site would miss this)', () => {
    expect(formatMode('MULTI_PART_LAB')).toBe('MULTI PART LAB');
  });
});
