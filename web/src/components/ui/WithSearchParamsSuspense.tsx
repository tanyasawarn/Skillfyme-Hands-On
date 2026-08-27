import { Suspense, type ComponentType } from 'react';
import { PageContainer, type PageContainerMaxWidth } from './PageContainer';
import { Loader } from './Loader';

/**
 * PLAN.md Phase 4's C9: `useSearchParams()` opts a subtree into
 * client-side rendering during prerender (Next.js requirement) --
 * `catalog/page.tsx` and `skills/page.tsx` each split their real
 * content into an `...Inner` component and wrapped it in an identically
 * -shaped `<Suspense fallback={<PageContainer><Loader/></PageContainer>}>`
 * boilerplate default export, differing only in the fallback's
 * maxWidth/spacing/label. A component, not a HOC function returning a
 * component, since both real call sites are the page's own default
 * export -- `export default withSearchParamsSuspense(Inner, {...})`
 * reads the same either way, but a plain component keeps this on the
 * same footing as every other shared UI piece in this file (testable
 * with the same render()-based pattern, no special "returns a
 * component" test setup).
 */
interface WithSearchParamsSuspenseProps {
  Component: ComponentType;
  loadingLabel: string;
  maxWidth?: PageContainerMaxWidth;
  spacing?: string;
}

export function WithSearchParamsSuspense({
  Component,
  loadingLabel,
  maxWidth,
  spacing = 'py-10',
}: WithSearchParamsSuspenseProps) {
  return (
    <Suspense
      fallback={
        <PageContainer maxWidth={maxWidth} spacing={spacing}>
          <Loader label={loadingLabel} />
        </PageContainer>
      }
    >
      <Component />
    </Suspense>
  );
}
