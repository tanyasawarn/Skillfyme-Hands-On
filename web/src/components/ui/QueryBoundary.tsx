import type { ReactNode } from 'react';
import { Loader } from './Loader';
import { Alert } from './Alert';
import { toUserFacingError } from '@/lib/error-message';

/**
 * PLAN.md Phase 4's C8: the isLoading/isError inline-conditional
 * pattern was byte-for-byte identical in `catalog/page.tsx` and
 * `history/page.tsx` -- `{isLoading && <Loader .../>}` then `{isError
 * && (IIFE calling toUserFacingError -> <Alert>)}`, both rendered
 * alongside the page's own header/hero (not a full-page replace) so
 * the chrome stays visible while data loads.
 *
 * Deliberately scoped to only this inline shape -- `catalog/
 * [activityVersionId]/page.tsx` and `attempts/[id]/page.tsx` use a
 * different early-return whole-page-replace style, already built from
 * the same shared `Loader`/`Alert`/`PageContainer` primitives in a
 * legitimately different structural shape (loading/error there means
 * "there is no page to show yet," not "the header is up, content is
 * pending"). Forcing both shapes into one component would repeat C7's
 * near-miss: unifying chrome that looks similar but serves genuinely
 * different layout needs.
 *
 * The success-case content itself (the `data && ...` blocks) stays
 * with each caller as `children` -- this component only owns the
 * loading/error rendering, matching the real duplication exactly
 * rather than inventing a broader data-fetching abstraction.
 */
interface QueryBoundaryProps {
  isLoading: boolean;
  isError: boolean;
  error?: unknown;
  loadingLabel: string;
  children: ReactNode;
}

export function QueryBoundary({ isLoading, isError, error, loadingLabel, children }: QueryBoundaryProps) {
  return (
    <>
      {isLoading && <Loader label={loadingLabel} />}
      {isError &&
        (() => {
          const { headline, detail } = toUserFacingError(error);
          return (
            <Alert className="mt-8" detail={detail}>
              {headline}
            </Alert>
          );
        })()}
      {children}
    </>
  );
}
