import { describe, it, expect } from 'vitest';
import { ATTEMPT_STATUS_META } from './attempt-status';
import type { AttemptStatus } from './api-client';

const ALL_STATUSES: AttemptStatus[] = [
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
];

describe('ATTEMPT_STATUS_META', () => {
  it('has an entry for every AttemptStatus value', () => {
    for (const status of ALL_STATUSES) {
      expect(ATTEMPT_STATUS_META[status]).toBeDefined();
    }
  });

  it('resolves the history.tsx vs attempts/[id].tsx color disagreement consistently', () => {
    // These 5 statuses previously rendered a different pill color
    // depending on which page you were on -- this is the single source
    // both pages now read from, so there is exactly one answer.
    expect(ATTEMPT_STATUS_META.PROVISIONING.variant).toBe('accent');
    expect(ATTEMPT_STATUS_META.IN_PROGRESS.variant).toBe('accent');
    expect(ATTEMPT_STATUS_META.SUSPENDED.variant).toBe('warning');
    expect(ATTEMPT_STATUS_META.EXPIRED.variant).toBe('warning');
    expect(ATTEMPT_STATUS_META.ABANDONED.variant).toBe('warning');
  });

  it('maps genuine success/failure states to success/danger', () => {
    expect(ATTEMPT_STATUS_META.PASSED.variant).toBe('success');
    expect(ATTEMPT_STATUS_META.COMPLETED.variant).toBe('success');
    expect(ATTEMPT_STATUS_META.FAILED.variant).toBe('danger');
    expect(ATTEMPT_STATUS_META.EVAL_FAILED.variant).toBe('danger');
    expect(ATTEMPT_STATUS_META.PROVISION_FAILED.variant).toBe('danger');
  });

  it('maps the not-yet-started state to muted', () => {
    expect(ATTEMPT_STATUS_META.CREATED.variant).toBe('muted');
  });
});
