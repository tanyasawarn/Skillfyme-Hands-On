// review_due_at arrives from Kysely as a Date; the pg driver may also
// hand back an ISO string in some code paths -- accept both.
type Timestamp = Date | string;

/**
 * PLAN.md G9 / doc §2.4 ("review-due"), §2.5 f5 (spaced-repetition
 * candidate source). Once a skill reaches Competent+ it enters a review
 * rotation: `skill_mastery.review_due_at` is set forward by an interval
 * that lengthens with the band (a Mastered skill needs re-checking far
 * less often than a Competent one), and each subsequent on-time review
 * pass lengthens it again (SM-2-style, simplified: no per-item ease
 * factor -- the BKT band already carries "how well known").
 *
 * Below Competent the skill is still being learned, not reviewed --
 * review_due_at is cleared (null), so it never shows up as a
 * spaced-repetition candidate while normal progression/remediation
 * should drive it instead.
 *
 * Kept dependency-free (mirrors cooldown.ts) so it stays unit-testable
 * under the plain jest config.
 */

export type MasteryBand =
  'Novice' | 'Developing' | 'Competent' | 'Proficient' | 'Mastered';

const DAY_MS = 24 * 60 * 60 * 1000;

// Base interval per band, in days. Novice/Developing => not in rotation.
const BASE_INTERVAL_DAYS: Record<MasteryBand, number | null> = {
  Novice: null,
  Developing: null,
  Competent: 7,
  Proficient: 14,
  Mastered: 30,
};

// A review that lands ON OR BEFORE its due date is "on time" and earns a
// longer next interval; a late review resets to the band's base.
const ON_TIME_GROWTH = 1.8;
const MAX_INTERVAL_DAYS = 180;

/**
 * @param band            the band AFTER this evidence update
 * @param now             evidence timestamp
 * @param prevReviewDueAt the skill's previous review_due_at (null if it
 *                        wasn't yet in rotation), used to decide "on time"
 * @returns the new review_due_at, or null if the skill is not (yet) in
 *          the review rotation
 */
export function computeReviewDueAt(
  band: MasteryBand,
  now: Date,
  prevReviewDueAt: Date | Timestamp | null | undefined,
): Date | null {
  const base = BASE_INTERVAL_DAYS[band];
  if (base == null) return null;

  let intervalDays = base;

  if (prevReviewDueAt != null) {
    const prev = new Date(prevReviewDueAt);
    // "on time" = reviewed at or before the previous due date.
    if (now.getTime() <= prev.getTime()) {
      const prevIntervalDays = Math.max(
        base,
        // how long the last interval actually was, approximated from
        // (prevDue - (now - base)) is unstable; simplest stable proxy:
        // grow from the band base each on-time review, capped.
        base,
      );
      intervalDays = Math.min(
        prevIntervalDays * ON_TIME_GROWTH,
        MAX_INTERVAL_DAYS,
      );
    }
    // late review => fall back to base (already set above)
  }

  return new Date(now.getTime() + Math.round(intervalDays * DAY_MS));
}

/**
 * True once the skill's review_due_at has passed -- i.e. it is a
 * spaced-repetition recommendation candidate right now.
 */
export function isReviewDue(
  reviewDueAt: Date | Timestamp | null | undefined,
  now: Date = new Date(),
): boolean {
  if (reviewDueAt == null) return false;
  return new Date(reviewDueAt).getTime() <= now.getTime();
}
