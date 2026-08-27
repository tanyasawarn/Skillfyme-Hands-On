import { describe, it, expect } from 'vitest';
import { render, screen } from '@testing-library/react';
import type { ReactNode } from 'react';
import { WithSearchParamsSuspense } from './WithSearchParamsSuspense';

// A component that suspends (throws a pending promise) on first render,
// matching what useSearchParams() triggers during prerender -- proves
// the fallback actually renders under a real Suspense boundary, not
// just that the fallback JSX is syntactically reachable.
function SuspendingInner(): ReactNode {
  throw new Promise(() => {});
}

function ResolvedInner() {
  return <p>real content</p>;
}

describe('WithSearchParamsSuspense', () => {
  it('renders the fallback Loader with the given label while the wrapped component suspends', () => {
    render(<WithSearchParamsSuspense Component={SuspendingInner} loadingLabel="Loading skills…" />);
    expect(screen.getByText('Loading skills…')).toBeInTheDocument();
  });

  it('renders the wrapped component once it resolves (not suspended)', () => {
    render(<WithSearchParamsSuspense Component={ResolvedInner} loadingLabel="Loading…" />);
    expect(screen.getByText('real content')).toBeInTheDocument();
  });

  it('passes maxWidth through to the fallback PageContainer', () => {
    const { container } = render(
      <WithSearchParamsSuspense Component={SuspendingInner} loadingLabel="Loading…" maxWidth="6xl" />,
    );
    expect(container.querySelector('.max-w-6xl')).toBeInTheDocument();
  });

  it('defaults spacing to py-10 when not given', () => {
    const { container } = render(
      <WithSearchParamsSuspense Component={SuspendingInner} loadingLabel="Loading…" />,
    );
    expect(container.querySelector('.py-10')).toBeInTheDocument();
  });
});
