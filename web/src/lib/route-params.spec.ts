import { describe, it, expect } from 'vitest';
import { COURSE_QUERY_PARAM } from './route-params';

describe('route-params', () => {
  it('COURSE_QUERY_PARAM matches the previously-triplicated literal (PLAN.md K3)', () => {
    expect(COURSE_QUERY_PARAM).toBe('course');
  });
});
