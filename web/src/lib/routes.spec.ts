import { describe, it, expect } from 'vitest';
import { attemptRoute, catalogEntryRoute } from './routes';

describe('routes (PLAN.md K2)', () => {
  it('attemptRoute builds /attempts/:id', () => {
    expect(attemptRoute('abc-123')).toBe('/attempts/abc-123');
  });

  it('catalogEntryRoute builds /catalog/:activityVersionId', () => {
    expect(catalogEntryRoute('xyz-789')).toBe('/catalog/xyz-789');
  });
});
