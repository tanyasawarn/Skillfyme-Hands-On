import type { ReactNode } from 'react';
import Link from 'next/link';

/**
 * PLAN.md Phase 4's C7: unifies the clickable-card *chrome* (border,
 * radius, background, hover behavior) across 3 real implementations
 * that had each grown their own hover/border treatment -- deliberately
 * does NOT dictate internal content layout. SkillCard (vertical,
 * multi-zone: badges/title/meta/footer), HistoryRow (horizontal row),
 * and the home dashboard cards (simple two-line) have genuinely
 * different internal structure; forcing one content shape onto all 3
 * would be a real visual regression, not a simplification, so callers
 * still compose their own children exactly as before -- only the
 * outer `<Link>`/`<div>` + its border/hover styling is shared.
 *
 * Three real chrome variants exist, not one:
 *  - 'lift' (SkillCard's live state): lift + shadow + border-color on
 *    hover, `min-height`, `flex-column` -- the richest hover treatment.
 *  - 'row' (HistoryRow): border-color-only hover, horizontal flex row
 *    -- was pure Tailwind with no custom CSS class; kept as Tailwind
 *    utilities here rather than inventing a new globals.css rule for
 *    a shape only one caller uses.
 *  - 'plain' (home dashboard cards): shadow-only hover, no lift/border
 *    change -- wraps the existing `.lms-card` class rather than
 *    duplicating it, since `.lms-card` is also used elsewhere for
 *    non-card-link static containers and must keep working unchanged.
 * A 4th, non-interactive `disabled` state (SkillCard's locked variant)
 * renders a plain `<div>` instead of a `Link` -- an unclickable card is
 * not the same DOM element as a clickable one, not just a style flag.
 */
export type CardLinkVariant = 'lift' | 'row' | 'plain';

interface CardLinkProps {
  variant: CardLinkVariant;
  href: string;
  /** Renders a non-interactive `<div>` instead of a `Link` (SkillCard's locked state) -- an unclickable card has no href to navigate to in practice, but takes one anyway so callers don't need a separate disabled-card element. */
  disabled?: boolean;
  children: ReactNode;
  className?: string;
}

const VARIANT_CLASS: Record<CardLinkVariant, string> = {
  lift: 'skill-card skill-card--live',
  row: 'flex items-center justify-between gap-4 rounded-lg border border-[var(--border)] bg-[var(--surface)] p-4 transition hover:border-[var(--accent)]',
  plain: 'lms-card block p-4',
};

const DISABLED_CLASS: Partial<Record<CardLinkVariant, string>> = {
  lift: 'skill-card skill-card--locked',
};

export function CardLink({ variant, href, disabled, children, className }: CardLinkProps) {
  const classes = [
    disabled ? (DISABLED_CLASS[variant] ?? VARIANT_CLASS[variant]) : VARIANT_CLASS[variant],
    className ?? '',
  ]
    .filter(Boolean)
    .join(' ');

  if (disabled) {
    return <div className={classes}>{children}</div>;
  }
  return (
    <Link href={href} className={classes}>
      {children}
    </Link>
  );
}
