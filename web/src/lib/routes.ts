/**
 * PLAN.md K2: route path builders for this app's parameterized routes.
 * Previously rebuilt as ad-hoc template strings at every call site
 * (`/attempts/${id}`, `/catalog/${activityVersionId}`) with no shared
 * source -- a route segment rename would require finding and updating
 * every literal template independently. Static routes (`/`, `/catalog`,
 * `/skills`, `/history`) aren't included: there's nothing to
 * parameterize, and `Sidebar.tsx`'s `NAV_ITEMS` already owns those.
 */
export function attemptRoute(attemptId: string): string {
  return `/attempts/${attemptId}`;
}

export function catalogEntryRoute(activityVersionId: string): string {
  return `/catalog/${activityVersionId}`;
}
