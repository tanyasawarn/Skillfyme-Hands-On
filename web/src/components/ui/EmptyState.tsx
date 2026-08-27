import type { ReactNode } from 'react';

/**
 * PLAN.md Phase 4's C3: the "no data yet" message hand-rolled at 6 real
 * sites (catalog/page.tsx, history/page.tsx, skills/page.tsx x1,
 * page.tsx x2, attempts/[id]/page.tsx's tasks list) -- `Loader` already
 * covers the loading half of this pattern (Phase 3 standardization);
 * this is the other half. Two real shapes exist today, not one:
 *  - a plain message line (`<p className="mt-* text-[var(--ink-soft)]">`),
 *    used at both page-top-level (mt-8) and nested-inside-a-section
 *    (mt-2) positions -- `className` overrides the default the same way
 *    `Loader`'s does, rather than appending and producing a conflicting
 *    margin pair.
 *  - a bordered card box (attempts/[id]/page.tsx's "No tasks for this
 *    activity"), a `variant="card"` since it's visually a distinct
 *    element, not a smaller/larger version of the message line.
 * Children, not a `message: string` prop, since one real call site
 * (history/page.tsx) embeds a `<Link>` inside the message.
 */
type EmptyStateVariant = 'message' | 'card';

interface EmptyStateProps {
  variant?: EmptyStateVariant;
  children: ReactNode;
  className?: string;
}

export function EmptyState({ variant = 'message', children, className }: EmptyStateProps) {
  if (variant === 'card') {
    return (
      <div
        className={
          className ??
          'lms-card flex items-center justify-center p-5 text-sm text-[var(--ink-soft)]'
        }
      >
        {children}
      </div>
    );
  }

  // className fully replaces the default (not appended) -- real call
  // sites disagree on more than just top margin (page.tsx's nested
  // empty states also need text-sm, unlike the page-top-level mt-8
  // sites), so a partial override would leave a stale text-[var(--ink-soft)]
  // baked in underneath whatever the caller actually wanted.
  return <p className={className ?? 'mt-8 text-[var(--ink-soft)]'}>{children}</p>;
}
