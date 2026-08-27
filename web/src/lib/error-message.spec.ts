import { describe, it, expect } from 'vitest';
import { toUserFacingError } from './error-message';
import { ApiError } from './api-client';

describe('toUserFacingError', () => {
  it('shows the backend exception filter message for a real API error', () => {
    const error = new ApiError(404, { statusCode: 404, message: 'activity abc123 not found', path: '/v1/practice/activities/abc123' });
    const result = toUserFacingError(error);
    expect(result.headline).toBe('activity abc123 not found');
  });

  it('falls back to ApiError.message when the body has no message field (e.g. a non-JSON body)', () => {
    const error = new ApiError(502, 'raw text body, no JSON');
    const result = toUserFacingError(error);
    expect(result.headline).toContain('API error 502');
  });

  it('treats a fetch-level network failure as the "is the server running?" case', () => {
    const error = new TypeError('Failed to fetch');
    const result = toUserFacingError(error);
    expect(result.headline).toMatch(/Could not reach practice-core/);
    expect(result.detail).toBe('Failed to fetch');
  });

  it('does not treat every TypeError as a network failure -- only fetch-related ones', () => {
    const error = new TypeError("Cannot read properties of undefined (reading 'foo')");
    const result = toUserFacingError(error);
    expect(result.headline).not.toMatch(/Could not reach practice-core/);
    expect(result.headline).toBe('Something went wrong.');
  });

  it('falls back to a generic message for a plain Error that is neither ApiError nor a fetch TypeError', () => {
    const result = toUserFacingError(new Error('unexpected bug'));
    expect(result.headline).toBe('Something went wrong.');
    expect(result.detail).toBe('unexpected bug');
  });

  it('SECURITY/CORRECTNESS: never crashes on a thrown non-Error value (string, object, undefined)', () => {
    expect(() => toUserFacingError('a raw string was thrown')).not.toThrow();
    expect(() => toUserFacingError({ weird: 'object' })).not.toThrow();
    expect(() => toUserFacingError(undefined)).not.toThrow();

    const result = toUserFacingError('a raw string was thrown');
    expect(result.headline).toBe('Something went wrong.');
    expect(result.detail).toBe('a raw string was thrown');
  });
});
