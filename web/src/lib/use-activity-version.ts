import { useQuery, type UseQueryOptions } from '@tanstack/react-query';
import { api, type CatalogEntryDetail } from './api-client';

/**
 * PLAN.md Phase 4's S1: the identical `useQuery({ queryKey: ['activity-
 * version', id], queryFn: () => api.getActivityVersion(id) })` shape
 * redeclared in 3 places. `history/page.tsx` needs the query *descriptor*
 * itself (not a called hook) since it batches N of these through
 * `useQueries()`, one per distinct activity_version_id on the page --
 * `activityVersionQueryOptions` is exported separately for that case,
 * with `useActivityVersion` as the single-id hook built on top of it,
 * so both real usages share one definition of the query shape rather
 * than the batched site re-inlining it a 4th way.
 *
 * `id` takes `string | undefined` (matching `attempts/[id]/page.tsx`'s
 * real case, where `attempt?.activity_version_id` isn't known until the
 * attempt itself has loaded) -- always ANDs `!!id` into `enabled`
 * regardless of what a caller passes, so a possibly-undefined id can
 * never accidentally fire a request for `undefined` even if a caller's
 * own `enabled` override forgets to check for it.
 */
export function activityVersionQueryOptions(id: string) {
  return {
    queryKey: ['activity-version', id] as const,
    queryFn: () => api.getActivityVersion(id),
  };
}

export function useActivityVersion(
  id: string | undefined,
  options?: Omit<Partial<UseQueryOptions<CatalogEntryDetail>>, 'queryKey' | 'queryFn'>,
) {
  return useQuery({
    ...activityVersionQueryOptions(id ?? ''),
    ...options,
    enabled: (options?.enabled ?? true) && !!id,
  });
}
