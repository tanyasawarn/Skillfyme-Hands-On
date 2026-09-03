import { ConflictException } from '@nestjs/common';
import type { Kysely } from 'kysely';
import type { Database } from '../../src/db/schema';
import { CatalogRepository } from '../../src/modules/catalog/catalog.repository';

/**
 * Doc §3.7 / §3.6 activity-version lifecycle state machine (PLAN.md M1.14
 * "minimal CMS ... publish-request only"):
 *   DRAFT -> IN_REVIEW -> APPROVED -> PUBLISHED
 * with IN_REVIEW/APPROVED able to fall back to DRAFT, and PUBLISHED
 * terminal. admin.controller.ts's review-decision / publish routes are
 * thin @Roles(ADMIN) wrappers over CatalogRepository.transitionVersionStatus;
 * the enforced invariant lives here in the ALLOWED_TRANSITIONS guard,
 * which rejects an illegal hop with ConflictException BEFORE any DB
 * access -- so this is exercisable with a db stub that throws if reached.
 *
 * This closes the gap where the admin CMS module had zero test coverage
 * of the §3.7 draft/review/approval separation-of-duties flow.
 */
describe('CatalogRepository.transitionVersionStatus — §3.7 lifecycle guard', () => {
  // transitionVersionStatus reaches .transaction() only on a LEGAL
  // transition; this throwing stub both stands in for the DB and proves
  // the guard short-circuits before it on the illegal paths.
  const throwingDb = {
    transaction: () => {
      throw new Error('DB_REACHED');
    },
  } as unknown as Kysely<Database>;

  const repo = new CatalogRepository(throwingDb);
  const NIL = '00000000-0000-0000-0000-000000000000';

  const ILLEGAL: Array<[string, string]> = [
    ['DRAFT', 'APPROVED'],
    ['DRAFT', 'PUBLISHED'], // the separation-of-duties bypass §3.7 exists to prevent
    ['IN_REVIEW', 'PUBLISHED'],
    ['APPROVED', 'IN_REVIEW'],
    ['PUBLISHED', 'DRAFT'],
    ['PUBLISHED', 'IN_REVIEW'],
    ['PUBLISHED', 'APPROVED'],
  ];

  it.each(ILLEGAL)(
    'rejects %s -> %s (ConflictException, DB untouched)',
    async (from, to) => {
      await expect(
        repo.transitionVersionStatus(NIL, from as never, to as never),
      ).rejects.toBeInstanceOf(ConflictException);
    },
  );

  const LEGAL: Array<[string, string]> = [
    ['DRAFT', 'IN_REVIEW'],
    ['IN_REVIEW', 'APPROVED'],
    ['IN_REVIEW', 'DRAFT'],
    ['APPROVED', 'PUBLISHED'],
    ['APPROVED', 'DRAFT'],
  ];

  it.each(LEGAL)(
    'permits %s -> %s past the guard (reaches DB layer)',
    async (from, to) => {
      await expect(
        repo.transitionVersionStatus(NIL, from as never, to as never),
      ).rejects.toThrow('DB_REACHED');
    },
  );
});
