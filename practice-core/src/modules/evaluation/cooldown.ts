/**
 * Doc §2.7: "after a failure, the same activity is not immediately
 * re-offered. Cooldown = min(24h × 2^(retry_count-1), 7d) OR cleared
 * early if the learner passes a remediation activity for the failed
 * sub-skill." This module implements the time-based half only --
 * the early-clear half is EvaluationService.clearCooldownsForRemediatedSkills
 * (evaluation.service.ts), which runs after every passing attempt and
 * clears cooldown_until on any other activity sharing a primary skill
 * with the one just passed. Kept in this dependency-free module rather
 * than folded into that method since the two halves have genuinely
 * different testability needs (this is pure date math; the early-clear
 * half needs a real DB query) -- see this file's own note below on why
 * that split exists.
 *
 * Scoped to the struggling-skill-itself remediation the recommendation
 * engine already generates today (recommendation.service.ts's own doc
 * comment: "not yet the full skill-DAG-ancestor walk... a reasonable
 * Phase 4 extension"), not the full ancestor-walk ladder -- that
 * narrower scope is what makes early-clear implementable now rather
 * than blocked on Phase 4's ancestor-walk work.
 *
 * The doc's retry_count is 1-indexed ("this is the learner's Nth
 * attempt"); this codebase's attempt.retry_index is 0-indexed (0 = a
 * genuine first attempt, not a retry). So retry_count for the attempt
 * that just failed = failedAttemptRetryIndex + 1: a first-ever failure
 * (retry_index=0, retry_count=1) gets exponent 0 -> the base 24h;
 * a second failure (retry_index=1, retry_count=2) gets exponent 1 -> 48h;
 * and so on up to the 7-day cap.
 *
 * Kept in its own dependency-free module (rather than inline in
 * evaluation.service.ts, which imports Kysely) so it stays testable
 * under this project's unit jest config, which has no
 * transformIgnorePatterns override for kysely's ESM build -- matching
 * why criteria.ts is likewise split out from evaluation.service.ts.
 */
const BASE_COOLDOWN_MS = 24 * 60 * 60 * 1000;
const MAX_COOLDOWN_MS = 7 * 24 * 60 * 60 * 1000;

export function computeCooldownUntil(
  failedAttemptRetryIndex: number,
  now: Date = new Date(),
): Date {
  const retryCount = failedAttemptRetryIndex + 1;
  const ms = Math.min(
    BASE_COOLDOWN_MS * 2 ** (retryCount - 1),
    MAX_COOLDOWN_MS,
  );
  return new Date(now.getTime() + ms);
}
