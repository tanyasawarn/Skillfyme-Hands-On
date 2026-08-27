import type { ReactNode } from 'react';

/**
 * PLAN.md Phase 4's C2: the `mx-auto max-w-* px-6 py-*` wrapper repeated
 * on every route -- real values disagree per page (max-w-3xl on 4
 * pages, max-w-4xl on history, max-w-6xl on catalog; py-10/py-12/pb-16
 * vertical spacing), so this takes both as props rather than hardcoding
 * one page's choice for everyone. `px-6` and `mx-auto` are the one part
 * every real site agrees on, so those stay fixed.
 */
export type PageContainerMaxWidth = '3xl' | '4xl' | '6xl';

const MAX_WIDTH_CLASS: Record<PageContainerMaxWidth, string> = {
  '3xl': 'max-w-3xl',
  '4xl': 'max-w-4xl',
  '6xl': 'max-w-6xl',
};

interface PageContainerProps {
  maxWidth?: PageContainerMaxWidth;
  /** Vertical padding/margin, e.g. 'py-10', 'py-12', 'pb-16' -- real pages disagree, so this is required rather than defaulted to one page's choice. */
  spacing: string;
  children: ReactNode;
  className?: string;
}

export function PageContainer({
  maxWidth = '3xl',
  spacing,
  children,
  className,
}: PageContainerProps) {
  const classes = ['mx-auto', MAX_WIDTH_CLASS[maxWidth], 'px-6', spacing, className ?? '']
    .filter(Boolean)
    .join(' ');
  return <div className={classes}>{children}</div>;
}
