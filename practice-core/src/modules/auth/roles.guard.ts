import {
  CanActivate,
  ExecutionContext,
  ForbiddenException,
  Injectable,
} from '@nestjs/common';
import { Reflector } from '@nestjs/core';
import type { Request } from 'express';
import type { Role } from './role';
import { ROLES_KEY } from './roles.decorator';

/** Runs after AuthGuard (req.auth is already set); enforces @Roles() where present. */
@Injectable()
export class RolesGuard implements CanActivate {
  constructor(private readonly reflector: Reflector) {}

  canActivate(context: ExecutionContext): boolean {
    const required = this.reflector.getAllAndOverride<Role[] | undefined>(
      ROLES_KEY,
      [context.getHandler(), context.getClass()],
    );
    if (!required || required.length === 0) return true;

    const req = context.switchToHttp().getRequest<Request>();
    if (!req.auth || !required.includes(req.auth.role)) {
      throw new ForbiddenException('insufficient role for this route');
    }
    return true;
  }
}
