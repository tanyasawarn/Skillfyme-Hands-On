import { describe, it, expect } from 'vitest';
import { masteryBandMeta, masteryBandVariant, masteryBandFillClassName } from './mastery';

describe('mastery bands (PLAN.md K6)', () => {
  it('preserves the exact colors both previously-independent maps already agreed on', () => {
    expect(masteryBandMeta('Novice')).toEqual({ variant: 'muted', fillClassName: 'bg-[var(--ink-soft)]' });
    expect(masteryBandMeta('Developing')).toEqual({ variant: 'warning', fillClassName: 'bg-[var(--warning)]' });
    expect(masteryBandMeta('Competent')).toEqual({ variant: 'accent', fillClassName: 'bg-[var(--accent)]' });
    expect(masteryBandMeta('Proficient')).toEqual({ variant: 'success', fillClassName: 'bg-[var(--success)]' });
    expect(masteryBandMeta('Mastered')).toEqual({ variant: 'success', fillClassName: 'bg-[var(--success)]' });
  });

  it('falls back to muted/ink-soft for an unrecognized band', () => {
    expect(masteryBandVariant('Unknown')).toBe('muted');
    expect(masteryBandFillClassName('Unknown')).toBe('bg-[var(--ink-soft)]');
  });

  it('masteryBandVariant and masteryBandFillClassName are consistent with masteryBandMeta', () => {
    for (const band of ['Novice', 'Developing', 'Competent', 'Proficient', 'Mastered']) {
      const meta = masteryBandMeta(band);
      expect(masteryBandVariant(band)).toBe(meta.variant);
      expect(masteryBandFillClassName(band)).toBe(meta.fillClassName);
    }
  });
});
