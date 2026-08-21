import { createParamDecorator, ExecutionContext } from '@nestjs/common';
import type { Request } from 'express';
import type { AuthClaims } from './auth.types';

/** req.auth is always set by AuthGuard before a controller method runs. */
export const AuthUser = createParamDecorator(
  (_: unknown, context: ExecutionContext): AuthClaims => {
    const req = context.switchToHttp().getRequest<Request>();
    return req.auth as AuthClaims;
  },
);
