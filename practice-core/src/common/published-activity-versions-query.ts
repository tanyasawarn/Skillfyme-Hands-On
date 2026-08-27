/**
 * PLAN.md Phase 4's S6: the `content.activity_version as av` ->
 * `content.activity as a` join, filtered to one tenant's PUBLISHED
 * versions, was independently rewritten 4 times across 2 files
 * (`catalog.repository.ts`'s `listPublishedCatalog`,
 * `recommendation.service.ts` x2). `catalog.repository.ts`'s
 * `listSkillDrivenCatalog` uses a related but genuinely different
 * shape -- a LEFT JOIN with the tenant/status checks as join
 * conditions rather than WHERE clauses, since an unmatched skill still
 * needs to appear in the result with null activity fields -- kept as
 * its own hand-written join rather than forced through this function.
 *
 * A single generic `T` (not `SelectQueryBuilder<DB, TB, O>` split into
 * 3 separate generic parameters), matching Kysely's own documented
 * `.$call<T>(func: (qb: this) => T): T` pattern for exactly this "add a
 * shared filter fragment onto an arbitrary caller-supplied query"
 * use case -- confirmed via real `tsc` errors that splitting the
 * generic into DB/TB/O parameters does NOT preserve a caller's actual
 * joined-table set through the function boundary (Kysely represents
 * each `.selectFrom()`/`.innerJoin()` chain as its own distinct
 * intersection type specific to that exact call site, e.g. a 2-join
 * call site and a 4-join call site produce genuinely different `DB`
 * shapes; splitting the generic collapses back to this function's own
 * declared constraint bounds instead of the caller's real, richer
 * type). Using one `T extends {...}` generic (Kysely's own idiom) fixed
 * this, confirmed against all 4 real call sites including the 2 that
 * join additional tables beyond `av`/`a`.
 */
export function publishedActivityVersionsQuery<
  T extends {
    where(lhs: 'a.tenant_id', op: '=', rhs: string): T;
    where(lhs: 'av.status', op: '=', rhs: string): T;
  },
>(qb: T, tenantId: string): T {
  return qb
    .where('a.tenant_id', '=', tenantId)
    .where('av.status', '=', 'PUBLISHED');
}
