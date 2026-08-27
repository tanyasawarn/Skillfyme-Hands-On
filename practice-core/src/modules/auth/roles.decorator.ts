import { SetMetadata } from '@nestjs/common';
import type { Role } from './role';

export const ROLES_KEY = 'roles';

/** Combined with RolesGuard; a route with no @Roles() is open to any authenticated role. */
export const Roles = (...roles: Role[]) => SetMetadata(ROLES_KEY, roles);
