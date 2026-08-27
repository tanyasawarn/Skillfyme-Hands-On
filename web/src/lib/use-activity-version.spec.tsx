import { describe, it, expect, vi } from 'vitest';
import { renderHook, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import type { ReactNode } from 'react';
import { activityVersionQueryOptions, useActivityVersion } from './use-activity-version';
import { api } from './api-client';

function wrapper({ children }: { children: ReactNode }) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return <QueryClientProvider client={client}>{children}</QueryClientProvider>;
}

describe('activityVersionQueryOptions', () => {
  it('builds the shared query descriptor (queryKey + queryFn) for a given id -- used directly by history.tsx\'s useQueries batching', () => {
    const opts = activityVersionQueryOptions('abc-123');
    expect(opts.queryKey).toEqual(['activity-version', 'abc-123']);
    expect(typeof opts.queryFn).toBe('function');
  });
});

describe('useActivityVersion', () => {
  it('does not fire the query when id is undefined (matches attempts/[id].tsx\'s prior manual enabled:!!attempt guard)', () => {
    const spy = vi.spyOn(api, 'getActivityVersion');
    renderHook(() => useActivityVersion(undefined), { wrapper });
    expect(spy).not.toHaveBeenCalled();
    spy.mockRestore();
  });

  it('fires the query when a real id is given', async () => {
    const spy = vi.spyOn(api, 'getActivityVersion').mockResolvedValue({
      id: 'v1',
    } as never);
    const { result } = renderHook(() => useActivityVersion('v1'), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(spy).toHaveBeenCalledWith('v1');
    spy.mockRestore();
  });

  it('never fires when id is undefined even if a caller passes enabled:true', () => {
    const spy = vi.spyOn(api, 'getActivityVersion');
    renderHook(() => useActivityVersion(undefined, { enabled: true }), { wrapper });
    expect(spy).not.toHaveBeenCalled();
    spy.mockRestore();
  });
});
