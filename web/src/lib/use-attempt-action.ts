import { useMutation, useQueryClient } from '@tanstack/react-query';

/**
 * PLAN.md Phase 4's S2: `startMutation`/`submitMutation` in
 * `attempts/[id]/page.tsx` were identical in shape --
 * `useMutation({ mutationFn: () => apiFn(id), onSuccess: () =>
 * queryClient.invalidateQueries({ queryKey: ['attempt', id] }) })` --
 * differing only in which API call they wrap. `revealMutation` in the
 * same file is NOT another instance of this: its `onSuccess` updates
 * local component state (`setRevealed`), not `invalidateQueries(['attempt', id])`,
 * a genuinely different shape, confirmed before assuming it belonged here too.
 */
export function useAttemptAction(attemptId: string, apiFn: (id: string) => Promise<unknown>) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: () => apiFn(attemptId),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['attempt', attemptId] }),
  });
}
