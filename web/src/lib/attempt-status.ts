import type { AttemptStatus } from './api-client';

export type StatusPillVariant = 'accent' | 'success' | 'warning' | 'danger' | 'muted';

/**
 * PLAN.md's K5 (Phase 2, confirmed live bug) folded into Phase 4's C1
 * work: history/page.tsx and attempts/[id]/page.tsx each declared their
 * own AttemptStatus -> color map, and disagreed on 5 of 14 statuses
 * (PROVISIONING accent-vs-warning, IN_PROGRESS accent-vs-success,
 * SUSPENDED/EXPIRED/ABANDONED warning-vs-muted) -- the same status
 * literally rendered a different pill color depending on which page you
 * were on. This is the one canonical source both pages (and the new
 * shared Badge component, C1) read from.
 *
 * Variant choices, reconciling the disagreement:
 *  - accent: an in-progress/transient infrastructure or learner-activity
 *    state, not yet a success or a problem (PROVISIONING, READY,
 *    IN_PROGRESS, SUBMITTED, EVALUATING) -- history.tsx's choice for
 *    PROVISIONING/IN_PROGRESS, kept over attempts/[id].tsx's
 *    warning/success (neither an in-progress infra step nor an
 *    unfinished attempt is itself a warning or an accomplishment yet).
 *  - success: attempt genuinely finished well (PASSED, COMPLETED).
 *  - danger: attempt or its infra genuinely failed (FAILED, EVAL_FAILED,
 *    PROVISION_FAILED).
 *  - warning: attempt reached a non-error terminal/paused state that
 *    still deserves the learner's attention (SUSPENDED, EXPIRED,
 *    ABANDONED) -- history.tsx's choice, kept over attempts/[id].tsx's
 *    muted (muted is reserved for the pre-work CREATED state, not a
 *    state that already needed the learner to do something and didn't).
 *  - muted: the attempt hasn't started yet (CREATED only).
 */
export const ATTEMPT_STATUS_META: Record<
  AttemptStatus,
  { label: string; variant: StatusPillVariant }
> = {
  CREATED: { label: 'Created', variant: 'muted' },
  PROVISIONING: { label: 'Provisioning', variant: 'accent' },
  READY: { label: 'Ready', variant: 'accent' },
  IN_PROGRESS: { label: 'In progress', variant: 'accent' },
  SUBMITTED: { label: 'Submitted', variant: 'accent' },
  EVALUATING: { label: 'Evaluating', variant: 'accent' },
  PASSED: { label: 'Passed', variant: 'success' },
  COMPLETED: { label: 'Completed', variant: 'success' },
  FAILED: { label: 'Failed', variant: 'danger' },
  EVAL_FAILED: { label: 'Evaluation failed', variant: 'danger' },
  PROVISION_FAILED: { label: 'Provisioning failed', variant: 'danger' },
  EXPIRED: { label: 'Expired', variant: 'warning' },
  ABANDONED: { label: 'Abandoned', variant: 'warning' },
  SUSPENDED: { label: 'Suspended', variant: 'warning' },
};
