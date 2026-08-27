import {
  CanActivate,
  ExecutionContext,
  ForbiddenException,
  Injectable,
} from '@nestjs/common';
import type { Request } from 'express';
import { AttemptRepository } from './attempt.repository';
import { isOwnedByCaller } from './attempt-ownership';

/**
 * PLAN.md Phase 3's S8: replaces `assertOwnedByCaller`, a private method
 * on AttemptController manually called at the top of 13 separate route
 * handlers, with a real NestJS guard applied once per route via
 * `@UseGuards(AttemptOwnershipGuard)`.
 *
 * Doc §9.1 T3 ("cross-learner data access"): every attempt-scoped route
 * must confirm the caller's own token owns this attempt, not just that
 * the attempt id exists -- mirrors the gateway's tokenEnvID != pathEnvID
 * check for the terminal WS path (orchestrator/internal/wsgateway).
 * Async (unlike RolesGuard, which only checks claims already on
 * req.auth) since ownership requires a real DB lookup against the
 * attempt's actual owner columns.
 *
 * Reads the attempt id from `req.params.id` -- every one of the 13
 * original call sites used the identical `@Param('id') id: string`
 * route-param name, confirmed by reading each handler before this
 * extraction; a future route needing a differently-named id param would
 * need its own guard instance or an extension point, not silently
 * misbehave, since Nest's route matching itself would 404 before this
 * guard runs if `:id` isn't in the route pattern at all.
 *
 * Runs after AuthGuard (global APP_GUARD, registered before this one in
 * app.module.ts's ordering) -- req.auth is guaranteed set by the time
 * this guard's canActivate runs, same precondition RolesGuard already
 * relies on.
 */
@Injectable()
export class AttemptOwnershipGuard implements CanActivate {
  constructor(private readonly attempts: AttemptRepository) {}

  async canActivate(context: ExecutionContext): Promise<boolean> {
    const req = context.switchToHttp().getRequest<Request>();
    // Express types route params as `string | string[]` (a route CAN in
    // principle capture an array segment); every real route this guard
    // protects has a single `:id` segment, so an array here would mean
    // something is structurally wrong with the request, not a valid
    // attempt id -- fail closed via ForbiddenException rather than
    // silently coercing an array to a string.
    const attemptId = req.params.id;
    if (typeof attemptId !== 'string') {
      throw new ForbiddenException('attempt does not belong to caller');
    }
    const auth = req.auth;

    const attempt = await this.attempts.findById(attemptId);
    if (!isOwnedByCaller(attempt, auth)) {
      throw new ForbiddenException('attempt does not belong to caller');
    }
    return true;
  }
}
