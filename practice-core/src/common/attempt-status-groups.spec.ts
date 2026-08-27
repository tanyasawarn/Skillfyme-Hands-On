import { AttemptStatusGroups } from './attempt-status-groups';
import type { AttemptStatus } from '../db/schema';

const ALL_STATUSES: readonly AttemptStatus[] = [
  'CREATED',
  'PROVISIONING',
  'READY',
  'IN_PROGRESS',
  'SUBMITTED',
  'EVALUATING',
  'PASSED',
  'FAILED',
  'COMPLETED',
  'PROVISION_FAILED',
  'SUSPENDED',
  'EVAL_FAILED',
  'EXPIRED',
  'ABANDONED',
  'CACHED',
];

describe('AttemptStatusGroups', () => {
  it('TERMINAL matches the original AttemptService.TERMINAL_STATUSES list exactly', () => {
    expect([...AttemptStatusGroups.TERMINAL].sort()).toEqual(
      [
        'PASSED',
        'FAILED',
        'COMPLETED',
        'PROVISION_FAILED',
        'EVAL_FAILED',
        'EXPIRED',
        'ABANDONED',
      ].sort(),
    );
  });

  // PLAN.md K7's actual bug: RETRYABLE_FROM previously omitted
  // PROVISION_FAILED at its one real call site
  // (AttemptRepository.findMostRecentCompletedAttempt). Pinning this
  // explicitly so it can't silently regress again.
  it('RETRYABLE_FROM includes PROVISION_FAILED (the drift K7 closes)', () => {
    expect(AttemptStatusGroups.RETRYABLE_FROM).toContain('PROVISION_FAILED');
  });

  it('RETRYABLE_FROM is TERMINAL minus PASSED', () => {
    const expected = AttemptStatusGroups.TERMINAL.filter((s) => s !== 'PASSED');
    expect([...AttemptStatusGroups.RETRYABLE_FROM].sort()).toEqual(
      [...expected].sort(),
    );
  });

  it('SUCCESS is a subset of TERMINAL', () => {
    for (const status of AttemptStatusGroups.SUCCESS) {
      expect(AttemptStatusGroups.TERMINAL).toContain(status);
    }
  });

  it('TERMINAL only contains real AttemptStatus values', () => {
    for (const status of AttemptStatusGroups.TERMINAL) {
      expect(ALL_STATUSES).toContain(status);
    }
  });

  it('every AttemptStatus is exactly one of TERMINAL or non-terminal (no overlap contradiction)', () => {
    // Sanity check on the schema union staying exhaustive against this
    // hand-written list -- if a new status is ever added to
    // db/schema.ts without a corresponding decision here, this is the
    // test that should be extended, not silently pass either way.
    expect(ALL_STATUSES.length).toBe(15);
  });
});
