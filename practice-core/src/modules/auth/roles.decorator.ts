import { SetMetadata } from '@nestjs/common';

export const ROLES_KEY = 'roles';

/** Combined with RolesGuard; a route with no @Roles() is open to any authenticated role. */
export const Roles = (...roles: string[]) => SetMetadata(ROLES_KEY, roles);
