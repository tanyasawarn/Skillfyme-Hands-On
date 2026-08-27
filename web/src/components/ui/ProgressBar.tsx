/**
 * PLAN.md Phase 4's C5: the mastery bar (page.tsx's home dashboard) and
 * criteria-breakdown bar (attempts/[id]/page.tsx's ResultPanel) are
 * structurally identical -- same track (`h-1.5 flex-1 overflow-hidden
 * rounded-full bg-[var(--inset)]`), same filled-inner-bar-by-percentage
 * shape. Two real differences: fill color varies per-row on the mastery
 * bar (by mastery band) but is a fixed accent on the criteria bar, and
 * only the criteria bar's inner fill has `rounded-full` +
 * `transition-[width]` -- both are real, not accidental, so both are
 * props rather than one component silently picking one page's choice.
 */
interface ProgressBarProps {
  /** 0-1 fraction, not a 0-100 percentage -- matches both real call sites' own source values (p_mastery, criterion.value). */
  value: number;
  /** Tailwind color utility for the filled portion, e.g. 'bg-[var(--accent)]'. */
  fillClassName: string;
  /** Criteria-breakdown bar rounds its inner fill + animates width changes; the mastery bar does neither. */
  animated?: boolean;
  className?: string;
}

export function ProgressBar({ value, fillClassName, animated = false, className }: ProgressBarProps) {
  const trackClasses = [
    'h-1.5 flex-1 overflow-hidden rounded-full bg-[var(--inset)]',
    className ?? '',
  ]
    .filter(Boolean)
    .join(' ');
  const fillClasses = ['h-full', animated ? 'rounded-full transition-[width]' : '', fillClassName]
    .filter(Boolean)
    .join(' ');

  return (
    <div className={trackClasses}>
      <div className={fillClasses} style={{ width: `${value * 100}%` }} />
    </div>
  );
}
