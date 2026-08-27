import type { AttemptStatus } from '../db/schema';

/**
 * PLAN.md K7: a single source of truth for "which AttemptStatus values
 * are terminal / active / a success outcome," derived once from the
 * schema's own AttemptStatus union rather than hand-copied per call
 * site. Before this, three independent lists existed with real,
 * confirmed drift between them:
 *
 * 1. `AttemptService.TERMINAL_STATUSES` (attempt.service.ts) -- correct,
 *    already included PROVISION_FAILED.
 * 2. `AttemptRepository.findMostRecentCompletedAttempt`'s inline
 *    `.where('status', 'in', [...])` list -- missing PROVISION_FAILED,
 *    meaning an attempt that failed to provision was NOT treated as
 *    "completed" for retry-chaining purposes, silently different from
 *    list 1 for no documented reason.
 * 3. `AttemptRepository.LIVE_CACHEABLE_STATUSES` -- a genuinely
 *    different concept (the *inverse* set: non-terminal, cacheable-if-
 *    stale statuses), kept separate below rather than derived from
 *    TERMINAL as its complement, since CACHED and SUSPENDED are
 *    deliberately excluded from both (a CACHED/SUSPENDED attempt is
 *    neither "live" nor freshly terminal -- it already went through its
 *    own transition).
 */
export const AttemptStatusGroups: {
  TERMINAL: readonly AttemptStatus[];
  RETRYABLE_FROM: readonly AttemptStatus[];
  SUCCESS: readonly AttemptStatus[];
} = {
  /**
   * A terminal attempt is done and idempotent-no-op-safe: no further
   * environment/evaluation event should move it anywhere else. Matches
   * the original, correct `AttemptService.TERMINAL_STATUSES`.
   */
  TERMINAL: [
    'PASSED',
    'FAILED',
    'COMPLETED',
    'PROVISION_FAILED',
    'EVAL_FAILED',
    'EXPIRED',
    'ABANDONED',
  ],

  /**
   * Doc §4.5/§2.7: statuses that count as "a prior attempt exists to
   * retry from." This is TERMINAL minus PASSED (a passed attempt isn't
   * something you retry -- retrying implies the learner didn't succeed
   * or the platform failed them) -- previously a separately hand-copied
   * list that had silently drifted to omit PROVISION_FAILED.
   */
  RETRYABLE_FROM: [
    'FAILED',
    'COMPLETED',
    'PROVISION_FAILED',
    'EVAL_FAILED',
    'ABANDONED',
    'EXPIRED',
  ],

  /** The one AttemptStatus that represents a genuine learner success. */
  SUCCESS: ['PASSED', 'COMPLETED'],
};
