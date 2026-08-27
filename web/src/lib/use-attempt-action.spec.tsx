import { describe, it, expect, vi } from 'vitest';
import { renderHook, waitFor, act } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import type { ReactNode } from 'react';
import { useAttemptAction } from './use-attempt-action';

function makeWrapper(client: QueryClient) {
  return function Wrapper({ children }: { children: ReactNode }) {
    return <QueryClientProvider client={client}>{children}</QueryClientProvider>;
  };
}

describe('useAttemptAction', () => {
  it('calls apiFn with the given attemptId when mutated', async () => {
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const apiFn = vi.fn().mockResolvedValue({ id: 'a1', status: 'IN_PROGRESS' });
    const { result } = renderHook(() => useAttemptAction('a1', apiFn), {
      wrapper: makeWrapper(client),
    });

    await act(async () => result.current.mutate());

    expect(apiFn).toHaveBeenCalledWith('a1');
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
  });

  it('invalidates the ["attempt", attemptId] query on success', async () => {
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const invalidateSpy = vi.spyOn(client, 'invalidateQueries');
    const apiFn = vi.fn().mockResolvedValue({ id: 'a1' });
    const { result } = renderHook(() => useAttemptAction('a1', apiFn), {
      wrapper: makeWrapper(client),
    });

    await act(async () => result.current.mutate());
    await waitFor(() => expect(result.current.isSuccess).toBe(true));

    expect(invalidateSpy).toHaveBeenCalledWith({ queryKey: ['attempt', 'a1'] });
  });
});
