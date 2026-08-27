import { NotFoundException } from '@nestjs/common';
import { findOrThrow } from './find-or-throw';

describe('findOrThrow', () => {
  it('returns the row when it is truthy', () => {
    const row = { id: 'x' };
    expect(findOrThrow(row, 'not found')).toBe(row);
  });

  it('throws NotFoundException with the given message when the row is null', () => {
    expect(() => findOrThrow(null, 'attempt x not found')).toThrow(
      NotFoundException,
    );
    expect(() => findOrThrow(null, 'attempt x not found')).toThrow(
      'attempt x not found',
    );
  });

  it('throws NotFoundException when the row is undefined', () => {
    expect(() => findOrThrow(undefined, 'course y not found')).toThrow(
      NotFoundException,
    );
  });

  it('does not reject falsy-but-present values (0, empty string, false) -- only null/undefined count as missing', () => {
    expect(findOrThrow(0, 'not found')).toBe(0);
    expect(findOrThrow('', 'not found')).toBe('');
    expect(findOrThrow(false, 'not found')).toBe(false);
  });
});
