'use client';

/**
 * Doc §7.5 step 38: "mentor offers the next hint level explicitly,
 * stating the score cost... 'I can give you a stronger hint -- that'll
 * cost about 3% on this task's score. Want it?' Transparency converts an
 * adversarial interaction into an informed choice." Two-step UI mirrors
 * the two-step API (preview = no side effect, reveal = commits + costs).
 * Revealed hints accumulate in local state so escalating through the
 * ladder shows the full trail, not just the latest hint.
 *
 * Extracted from attempts/[id]/page.tsx so the PRODUCTION_SIM rail
 * (components/sim/SimShell.tsx) reuses the exact same hint UX as the
 * GUIDED_LAB rail rather than forking it.
 */
import { useState } from 'react';
import { useMutation, useQuery } from '@tanstack/react-query';
import type { HintReveal } from '@/lib/api-client';
import { api } from '@/lib/api-client';
import { Button } from '@/components/ui/Button';
import { formatPercent } from '@/lib/format';

export function HintPanel({ attemptId, taskKey }: { attemptId: string; taskKey: string }) {
  const [revealed, setRevealed] = useState<HintReveal[]>([]);

  const { data: preview, isLoading } = useQuery({
    queryKey: ['hint-preview', attemptId, taskKey, revealed.length],
    queryFn: () => api.previewHint(attemptId, taskKey),
  });

  const revealMutation = useMutation({
    mutationFn: () => api.revealHint(attemptId, taskKey),
    onSuccess: (hint) => setRevealed((prev) => [...prev, hint]),
  });

  return (
    <div className="mt-2.5">
      {revealed.map((hint) => (
        <p key={hint.nextLevel} className="lms-inset-field mt-1.5 p-2.5 text-xs text-[var(--ink-muted)]">
          <span className="font-semibold text-[var(--ink-soft)]">Hint L{hint.nextLevel}:</span> {hint.text}
        </p>
      ))}

      {!isLoading && preview && (
        <Button
          onClick={() => revealMutation.mutate()}
          disabled={revealMutation.isPending}
          variant="outline"
          size="sm"
          className="mt-2 min-w-0 w-full"
        >
          {revealMutation.isPending ? 'Getting hint…' : `Get a hint (−${formatPercent(preview.penalty)})`}
        </Button>
      )}

      {!isLoading && !preview && revealed.length > 0 && (
        <p className="mt-2 text-xs text-[var(--ink-soft)]">No more hints for this task.</p>
      )}
    </div>
  );
}
