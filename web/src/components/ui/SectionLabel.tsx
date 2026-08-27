import type { ReactNode } from 'react';

/**
 * PLAN.md C6: wraps the already-centralized `.font-mono-label` CSS
 * class (globals.css) so the heading level (h2/h3/p/span/label) stops
 * being chosen ad hoc per call site -- confirmed via grep that all 5
 * element types were in real use across 7 files with no consistent
 * rule for which to pick where. `as` defaults to `'h2'`, the most
 * common real usage (section headings on the home/catalog/attempt-
 * detail pages).
 */
type SectionLabelTag = 'h2' | 'h3' | 'p' | 'span' | 'label';

interface SectionLabelProps {
  as?: SectionLabelTag;
  children: ReactNode;
  className?: string;
  htmlFor?: string;
}

export function SectionLabel({ as = 'h2', children, className, htmlFor }: SectionLabelProps) {
  const classes = ['font-mono-label', className ?? ''].filter(Boolean).join(' ');
  const Tag = as;
  return (
    <Tag className={classes} {...(as === 'label' ? { htmlFor } : {})}>
      {children}
    </Tag>
  );
}
