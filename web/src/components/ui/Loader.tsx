/**
 * Phase 3 standardization: extracted from the identical
 * `<p className="mt-8 text-[var(--ink-muted)]">Loading …</p>` pattern
 * duplicated independently across catalog/page.tsx, history/page.tsx,
 * skills/page.tsx, and the home page -- `label` and the top margin
 * (mt-8 vs mt-10) were the only things that ever varied. `className`
 * fully REPLACES the default `mt-8` spacing (not appended) so a caller
 * can override it without producing a conflicting `mt-8 mt-10` class
 * pair; text color always stays the shared token.
 */
interface LoaderProps {
  label: string;
  className?: string;
}

export function Loader({ label, className }: LoaderProps) {
  return <p className={`${className ?? 'mt-8'} text-[var(--ink-muted)]`}>{label}</p>;
}
