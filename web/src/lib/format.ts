/**
 * PLAN.md Phase 4's U1: `formatPercent()`/`formatCurrency()`, closing
 * 7+ independently-inlined score/percentage/cost expressions using two
 * different rounding strategies (`.toFixed(0)` vs `Math.round(...)`).
 * Both round identically for the positive values every real call site
 * ever passes (scores, mastery fractions, penalties are all >=0), so
 * this wasn't a live numeric-divergence bug like K5/S7 -- but both
 * independently produce `"-0"` for a value close enough to zero
 * (`(-0.001).toFixed(0)` -> `"-0"`, `Math.round(-0.4)` -> `-0`), a real
 * cosmetic bug ("-0%" instead of "0%") that a shared, tested function
 * closes for every call site at once instead of needing 6 independent
 * fixes.
 */

/** value is a 0-1 fraction (matches every real call site's own source: p_mastery, final_score, penalty, criterion.value). */
export function formatPercent(value: number): string {
  const rounded = Math.round(value * 100);
  return `${rounded === 0 ? 0 : rounded}%`;
}

export function formatCurrency(value: number): string {
  return `$${value.toFixed(2)}`;
}

/**
 * PLAN.md's U2: `.replace('_', ' ')` (first underscore only) vs
 * `.replace(/_/g, ' ')` (every underscore) were both in real use across
 * 4 real call sites -- produces identical output for every mode value
 * that exists today (`GUIDED_LAB`/`PRODUCTION_SIM`/`PROJECT`, each with
 * exactly one underscore), so not a live bug yet, but a genuine
 * correctness landmine: any future mode value with 2+ underscores
 * (e.g. a hypothetical `MULTI_PART_LAB`) would silently render wrong on
 * whichever call sites still used the single-replace form.
 */
export function formatMode(mode: string): string {
  return mode.replace(/_/g, ' ');
}
