import { BadRequestException } from '@nestjs/common';

/**
 * Phase 3 (1.6). Same blocked-action throw shape the attempt module uses
 * (attempt-error.ts) — a BadRequestException with a typed `reasons[]`
 * body so the controller layer and the web client see a stable contract.
 */
export interface ProjectErrorReason {
  code: string;
  message: string;
  context?: Record<string, unknown>;
}

export function projectError(
  code: string,
  message: string,
  context?: Record<string, unknown>,
): never {
  const reason: ProjectErrorReason = {
    code,
    message,
    ...(context ? { context } : {}),
  };
  throw new BadRequestException({ message, reasons: [reason] });
}
