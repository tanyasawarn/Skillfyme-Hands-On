import type { BadgeVariant } from '@/components/ui/Badge';

/**
 * PLAN.md K6: doc §2.4's 5 mastery bands (Novice/Developing/Competent/
 * Proficient/Mastered), previously duplicated as two independently
 * hand-maintained maps with two different representations -- `page.tsx`'s
 * `BAND_COLOR` (raw background-color Tailwind classes for `ProgressBar`'s
 * fill) and `skills/page.tsx`'s `BAND_VARIANT` (`Badge` variant names).
 * Both single-sourced here from the same band->meta table so the two
 * pages' color choices for a given band can't silently drift apart --
 * confirmed the two existing maps already agreed color-for-color
 * (Novice=muted/ink-soft, Developing=warning, Competent=accent,
 * Proficient/Mastered=success) before consolidating, so this preserves
 * current behavior exactly rather than picking a new mapping.
 */
export interface MasteryBandMeta {
  variant: BadgeVariant;
  /** Tailwind background-color class for a filled bar/indicator (ProgressBar's `fillClassName`). */
  fillClassName: string;
}

const MASTERY_BAND_META: Record<string, MasteryBandMeta> = {
  Novice: { variant: 'muted', fillClassName: 'bg-[var(--ink-soft)]' },
  Developing: { variant: 'warning', fillClassName: 'bg-[var(--warning)]' },
  Competent: { variant: 'accent', fillClassName: 'bg-[var(--accent)]' },
  Proficient: { variant: 'success', fillClassName: 'bg-[var(--success)]' },
  Mastered: { variant: 'success', fillClassName: 'bg-[var(--success)]' },
};

const FALLBACK: MasteryBandMeta = { variant: 'muted', fillClassName: 'bg-[var(--ink-soft)]' };

export function masteryBandMeta(band: string): MasteryBandMeta {
  return MASTERY_BAND_META[band] ?? FALLBACK;
}

export function masteryBandVariant(band: string): BadgeVariant {
  return masteryBandMeta(band).variant;
}

export function masteryBandFillClassName(band: string): string {
  return masteryBandMeta(band).fillClassName;
}
