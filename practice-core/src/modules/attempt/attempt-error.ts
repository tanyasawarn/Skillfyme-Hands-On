import { BadRequestException } from '@nestjs/common';

/**
 * Machine-readable codes for every way a learner-facing attempt action
 * can be blocked. Before this, every guard threw a bare interpolated
 * string (attempt.service.ts's provision/submit/connect guards) or, at
 * best, an untyped `reasons: string[]` (createAttempt, forwarding
 * EligibilityService.check()'s prose array) -- a UI could only show the
 * message verbatim, never branch on *why* (e.g. show a "wait N minutes"
 * countdown for COOLDOWN_ACTIVE vs. a "resume your other lab" link for
 * CONCURRENT_QUOTA_EXCEEDED). One code per distinct blocking reason
 * across both EligibilityService and every AttemptService status guard.
 */
export type AttemptErrorCode =
  | 'ACTIVITY_NOT_PUBLISHED'
  | 'PREREQUISITE_NOT_MET'
  | 'COOLDOWN_ACTIVE'
  | 'CONCURRENT_QUOTA_EXCEEDED'
  | 'INVALID_STATE_TRANSITION'
  | 'ATTEMPT_NOT_FOUND'
  | 'NO_ENVIRONMENT'
  | 'ATTEMPT_VANISHED';

export interface AttemptErrorReason {
  code: AttemptErrorCode;
  message: string;
  /** Free-form context for debugging/UI -- e.g. { currentStatus, expectedStatus } or { cooldownUntil }. */
  context?: Record<string, unknown>;
}

/**
 * The structured body every blocked-action response now carries. `reasons`
 * is always non-empty and always the thing a client should branch/render
 * on; `message` stays as a single human-readable summary (the first
 * reason's message, or a fixed summary when there are several) so
 * existing code reading `error.body.message` doesn't break.
 */
export interface AttemptErrorBody {
  message: string;
  reasons: AttemptErrorReason[];
}

/** Throws a BadRequestException whose body is an AttemptErrorBody -- the one shape every blocked-action throw site in this module now uses. */
export function attemptError(reasons: AttemptErrorReason[]): never {
  if (reasons.length === 0) {
    throw new Error('attemptError() requires at least one reason');
  }
  const body: AttemptErrorBody = {
    message:
      reasons.length === 1
        ? reasons[0].message
        : reasons.map((r) => r.message).join('; '),
    reasons,
  };
  throw new BadRequestException(body);
}

export function singleAttemptError(
  code: AttemptErrorCode,
  message: string,
  context?: Record<string, unknown>,
): never {
  return attemptError([{ code, message, context }]);
}
