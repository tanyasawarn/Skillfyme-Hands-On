import { NotFoundException } from '@nestjs/common';

/**
 * PLAN.md Phase 4's U5: the "look up a row, throw if missing" pattern
 * was hand-rolled 9 real times across 7 files -- 6 of them (`attempt.
 * service.ts` x4, `hint.service.ts`, `workspace-file.service.ts`) threw
 * `BadRequestException` (400) for "attempt X not found," while the
 * other 3 (`artifact.service.ts`, `curriculum.controller.ts`,
 * `catalog.controller.ts`) threw `NotFoundException` (404) for the same
 * semantic condition on different resources -- a real API contract
 * inconsistency, not just style: "this resource does not exist" is a
 * 404 by HTTP semantics, and `all-exceptions.filter.ts`'s own doc
 * comment confirms every thrown `HttpException`'s status is treated as
 * an intentional, safe-to-surface decision, meaning the 6 `400`s were a
 * real (if minor) API inconsistency callers could observe, not an
 * implementation detail. User-confirmed direction: standardize on 404
 * (`NotFoundException`) everywhere `findOrThrow` is used, checked first
 * for any test/frontend dependency on the existing 400s (none found).
 */
export function findOrThrow<T>(row: T | null | undefined, message: string): T {
  if (row === null || row === undefined) {
    throw new NotFoundException(message);
  }
  return row;
}
