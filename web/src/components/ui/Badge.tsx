import type { ReactNode } from 'react';

/**
 * PLAN.md Phase 4's C1: unifies 3 parallel implementations that had
 * diverged -- the `.lms-pill` + per-page status maps (history.tsx,
 * attempts/[id].tsx, catalog.tsx), `.skill-card-badge` (catalog.tsx's
 * SkillCard), and TaskStatusIcon (attempts/[id].tsx, a small circular
 * glyph, not a labeled pill). All three ultimately draw from the same
 * 5 semantic color tokens (accent/success/warning/danger/muted), so one
 * component with a `shape` prop covers both the labeled-pill and
 * icon-only-circle cases rather than needing two components. See
 * lib/attempt-status.ts's own doc comment for the real color-disagreement
 * bug this closes (K5) as part of building this.
 */
export type BadgeVariant = 'accent' | 'success' | 'warning' | 'danger' | 'muted' | 'outline';
export type CircleBadgeVariant = Exclude<BadgeVariant, 'outline'>;

interface PillBadgeProps {
  shape?: 'pill';
  variant: BadgeVariant;
  /** Optional leading icon -- skill-card-badge's live/locked SVGs; omit for a text-only pill. */
  icon?: ReactNode;
  children: ReactNode;
  className?: string;
}

interface CircleBadgeProps {
  shape: 'circle';
  /** 'outline' has no circle-shape use case today (skill-card-badge's locked style is a pill-only look). */
  variant: CircleBadgeVariant;
  children: ReactNode;
  className?: string;
}

type BadgeProps = PillBadgeProps | CircleBadgeProps;

const PILL_VARIANT_CLASS: Record<BadgeVariant, string> = {
  accent: 'lms-pill--accent',
  success: 'lms-pill--success',
  warning: 'lms-pill--warning',
  danger: 'lms-pill--danger',
  muted: 'lms-pill--muted',
  outline: 'lms-pill--outline',
};

// TaskStatusIcon's circle shape used raw Tailwind arbitrary-value
// classes against the same CSS custom properties .lms-pill's variant
// classes use, rather than a shared class -- kept as inline classes
// here (not a new CSS class) since introducing globals.css churn is
// out of scope for what C1 asks for (unify the *components*, not
// redesign the token system, which is T1-T4's separate scope).
const CIRCLE_VARIANT_CLASS: Record<CircleBadgeVariant, string> = {
  accent: 'bg-[var(--accent-soft)] text-[var(--accent-deep)]',
  success: 'bg-[var(--success-soft)] text-[var(--success)]',
  warning: 'bg-[var(--warning-soft)] text-[var(--warning)]',
  danger: 'bg-[var(--danger-soft)] text-[var(--danger)]',
  muted: 'bg-[var(--inset)] text-[var(--ink-soft)]',
};

export function Badge(props: BadgeProps) {
  if (props.shape === 'circle') {
    const classes = [
      'flex h-4 w-4 shrink-0 items-center justify-center rounded-full text-[10px]',
      CIRCLE_VARIANT_CLASS[props.variant],
      props.className ?? '',
    ]
      .filter(Boolean)
      .join(' ');
    return <span className={classes}>{props.children}</span>;
  }

  const classes = [
    'lms-pill',
    PILL_VARIANT_CLASS[props.variant],
    props.icon ? 'lms-pill--with-icon' : '',
    props.className ?? '',
  ]
    .filter(Boolean)
    .join(' ');
  return (
    <span className={classes}>
      {props.icon}
      {props.children}
    </span>
  );
}
