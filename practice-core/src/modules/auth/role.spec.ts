import { isRole, Role, ROLE_VALUES } from './role';

describe('Role / isRole', () => {
  it('ROLE_VALUES contains exactly the 3 real roles used in this codebase', () => {
    expect([...ROLE_VALUES].sort()).toEqual(
      ['admin', 'author', 'learner'].sort(),
    );
  });

  it.each(['learner', 'author', 'admin'])(
    'accepts the real role %s',
    (value) => {
      expect(isRole(value)).toBe(true);
    },
  );

  it('rejects a typo of a real role', () => {
    expect(isRole('admni')).toBe(false);
  });

  it('rejects an empty string', () => {
    expect(isRole('')).toBe(false);
  });

  it('rejects a role string with different casing', () => {
    expect(isRole('Admin')).toBe(false);
  });

  it('narrows the type on a true result (compile-time check via usage, not a runtime assertion)', () => {
    const value: string = 'admin';
    if (isRole(value)) {
      const role: Role = value;
      expect(role).toBe(Role.ADMIN);
    } else {
      throw new Error('expected isRole to accept a valid role');
    }
  });
});
