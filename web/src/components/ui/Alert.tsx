import type { ReactNode } from 'react';

/**
 * Phase 3 standardization: extracted from the near-verbatim duplicated
 * error banner in catalog/page.tsx and history/page.tsx (identical
 * border/background/text token usage, identical structure -- a message
 * line plus an optional collapsed detail block). Only one variant today
 * ('danger') since that's the only one any call site currently needs;
 * add more variants (info/warning) here if a future page needs them,
 * rather than hand-rolling another one-off div.
 */
interface AlertProps {
  variant?: 'danger';
  children: ReactNode;
  /** Raw technical detail (e.g. String(error)) shown in a monospace block below the main message -- optional, matches the existing `<pre>{String(error)}</pre>` pattern both duplicated banners had. */
  detail?: string;
  className?: string;
}

const VARIANT_CLASS: Record<NonNullable<AlertProps['variant']>, string> = {
  danger: 'border-[var(--danger)] bg-[var(--danger-soft)] text-[var(--danger)]',
};

export function Alert({ variant = 'danger', children, detail, className }: AlertProps) {
  return (
    <div
      className={['rounded-md border p-4 text-sm', VARIANT_CLASS[variant], className ?? '']
        .filter(Boolean)
        .join(' ')}
    >
      {children}
      {detail && <pre className="mt-2 whitespace-pre-wrap text-xs opacity-70">{detail}</pre>}
    </div>
  );
}
