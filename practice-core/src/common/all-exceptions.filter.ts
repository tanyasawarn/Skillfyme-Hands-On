import {
  ArgumentsHost,
  Catch,
  ExceptionFilter,
  HttpException,
  HttpStatus,
  Logger,
} from '@nestjs/common';
import type { Request, Response } from 'express';

/**
 * Phase 3 standardization: this codebase's controllers/services already
 * throw NestJS's built-in HttpException subclasses consistently
 * (BadRequestException, ForbiddenException, NotFoundException --
 * confirmed by survey, no ad-hoc `throw new Error()` in that layer). The
 * real gap this filter closes is everything that ISN'T an HttpException:
 * a Kysely `executeTakeFirstOrThrow()` miss (21 call sites across
 * src/modules/), a DB connection failure, or any other unexpected
 * exception -- these previously fell through to Nest's built-in default
 * handler, which (outside NODE_ENV=production tuning most apps never
 * bother to configure) can leak the raw error message and occasionally
 * structural detail about the failure back to the client.
 *
 * Two distinct paths, matching the two distinct trust levels of the
 * error source:
 *   - HttpException: the code that threw it already decided this
 *     message is safe and meaningful for the caller to see (that's what
 *     throwing NotFoundException('activity not found') IS -- an
 *     intentional, safe-to-surface message). Pass its status and
 *     message straight through, just wrapped in one consistent envelope
 *     shape instead of whatever ad-hoc shape existed before.
 *   - anything else: untrusted by definition -- the code that threw it
 *     was NOT written with "this message is safe to show a caller" in
 *     mind (a DB driver error, a null-pointer-shaped bug, a third-party
 *     library's internal exception). Always collapses to a fixed,
 *     generic 500 message. The real error is logged server-side in
 *     full (message + stack), never returned to the client.
 */
@Catch()
export class AllExceptionsFilter implements ExceptionFilter {
  private readonly logger = new Logger('ExceptionFilter');

  catch(exception: unknown, host: ArgumentsHost): void {
    const ctx = host.switchToHttp();
    const response = ctx.getResponse<Response>();
    const request = ctx.getRequest<Request>();

    if (exception instanceof HttpException) {
      const status = exception.getStatus();
      const exceptionResponse = exception.getResponse();
      // NestJS's built-in HttpExceptions already carry a structured
      // response body (e.g. ValidationPipe's { message: string[] }) --
      // preserve that shape under `detail` rather than collapsing it to
      // a single string and losing per-field validation messages, the
      // one case where the existing shape carries real information this
      // envelope must not discard.
      const message =
        typeof exceptionResponse === 'string'
          ? exceptionResponse
          : ((exceptionResponse as { message?: string })?.message ??
            exception.message);

      // 5xx HttpExceptions (rare, but a controller/service CAN throw
      // InternalServerErrorException deliberately) still deserve a
      // server-side log -- a caller-visible 500 is exactly the class of
      // failure that should show up in logs, unlike a routine 404/403.
      if (status >= HttpStatus.INTERNAL_SERVER_ERROR) {
        this.logger.error(
          `${request.method} ${request.url} -> ${status}: ${exception.message}`,
          exception.stack,
        );
      }

      response.status(status).json({
        statusCode: status,
        path: request.url,
        message,
        ...(typeof exceptionResponse === 'object' && exceptionResponse !== null
          ? { detail: exceptionResponse }
          : {}),
      });
      return;
    }

    // Not an HttpException at all -- the untrusted path. Full detail
    // logged server-side (this is the one place in the app that should
    // see every unexpected exception's real message and stack, so an
    // on-call engineer can actually diagnose it); the client gets a
    // fixed, safe, generic message and nothing else.
    const err = exception instanceof Error ? exception : undefined;
    this.logger.error(
      `${request.method} ${request.url} -> 500 (unhandled): ${err?.message ?? String(exception)}`,
      err?.stack,
    );

    response.status(HttpStatus.INTERNAL_SERVER_ERROR).json({
      statusCode: HttpStatus.INTERNAL_SERVER_ERROR,
      path: request.url,
      message: 'Internal server error',
    });
  }
}
