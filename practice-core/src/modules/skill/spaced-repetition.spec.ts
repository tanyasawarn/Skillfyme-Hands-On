import { computeReviewDueAt, isReviewDue } from './spaced-repetition';

describe('spaced-repetition (PLAN.md G9 / doc §2.4 review-due)', () => {
  const now = new Date('2026-01-01T00:00:00Z');
  const days = (d: number) => d * 24 * 60 * 60 * 1000;

  it('does not put a below-Competent skill into the review rotation', () => {
    expect(computeReviewDueAt('Novice', now, null)).toBeNull();
    expect(computeReviewDueAt('Developing', now, null)).toBeNull();
  });

  it('sets a base interval per band on first entry into rotation', () => {
    const c = computeReviewDueAt('Competent', now, null)!;
    const p = computeReviewDueAt('Proficient', now, null)!;
    const m = computeReviewDueAt('Mastered', now, null)!;
    expect(c.getTime() - now.getTime()).toBe(days(7));
    expect(p.getTime() - now.getTime()).toBe(days(14));
    expect(m.getTime() - now.getTime()).toBe(days(30));
  });

  it('lengthens the interval on an on-time review (reviewed before due)', () => {
    // previously due 3 days from now; reviewing now = on time.
    const prevDue = new Date(now.getTime() + days(3));
    const next = computeReviewDueAt('Competent', now, prevDue)!;
    // base 7 * 1.8 growth
    expect(next.getTime() - now.getTime()).toBe(Math.round(days(7 * 1.8)));
  });

  it('falls back to the band base on a late review (reviewed after due)', () => {
    const prevDue = new Date(now.getTime() - days(5)); // overdue
    const next = computeReviewDueAt('Proficient', now, prevDue)!;
    expect(next.getTime() - now.getTime()).toBe(days(14));
  });

  it('drops a skill out of rotation if it slips below Competent', () => {
    const prevDue = new Date(now.getTime() + days(2));
    expect(computeReviewDueAt('Developing', now, prevDue)).toBeNull();
  });

  it('isReviewDue: null => false, future => false, past => true', () => {
    expect(isReviewDue(null, now)).toBe(false);
    expect(isReviewDue(new Date(now.getTime() + days(1)), now)).toBe(false);
    expect(isReviewDue(new Date(now.getTime() - days(1)), now)).toBe(true);
    expect(isReviewDue(now.toISOString(), now)).toBe(true); // string form
  });
});
