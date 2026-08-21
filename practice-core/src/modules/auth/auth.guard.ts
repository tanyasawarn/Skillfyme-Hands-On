import {
  CanActivate,
  ExecutionContext,
  Injectable,
  UnauthorizedException,
} from '@nestjs/common';
import { Reflector } from '@nestjs/core';
import { JwtService } from '@nestjs/jwt';
import type { Request } from 'express';
import type { AuthClaims } from './auth.types';
import { IS_PUBLIC_KEY } from './public.decorator';

/**
 * Applied globally (see app.module.ts APP_GUARD). Verifies the bearer JWT
 * and attaches the resulting claims to req.auth; controllers must read
 * user_id/tenant_id from there, never from the request body/query, or a
 * caller can act as any user by just passing a different id.
 */
@Injectable()
export class AuthGuard implements CanActivate {
  constructor(
    private readonly jwt: JwtService,
    private readonly reflector: Reflector,
  ) {}

  canActivate(context: ExecutionContext): boolean {
    const isPublic = this.reflector.getAllAndOverride<boolean>(IS_PUBLIC_KEY, [
      context.getHandler(),
      context.getClass(),
    ]);
    if (isPublic) return true;

    const req = context.switchToHttp().getRequest<Request>();
    const header = req.headers.authorization;
    if (!header?.startsWith('Bearer ')) {
      throw new UnauthorizedException('missing bearer token');
    }

    try {
      const claims = this.jwt.verify<AuthClaims>(
        header.slice('Bearer '.length),
      );
      if (!claims.userId || !claims.tenantId || !claims.role) {
        throw new UnauthorizedException('token missing required claims');
      }
      req.auth = {
        userId: claims.userId,
        tenantId: claims.tenantId,
        role: claims.role,
      };
      return true;
    } catch {
      throw new UnauthorizedException('invalid or expired token');
    }
  }
}
