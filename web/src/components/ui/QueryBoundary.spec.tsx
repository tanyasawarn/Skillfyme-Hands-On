import { describe, it, expect } from 'vitest';
import { render, screen } from '@testing-library/react';
import { QueryBoundary } from './QueryBoundary';

describe('QueryBoundary', () => {
  it('shows the Loader with the given label while isLoading', () => {
    render(
      <QueryBoundary isLoading isError={false} loadingLabel="Loading catalog…">
        <p>content</p>
      </QueryBoundary>,
    );
    expect(screen.getByText('Loading catalog…')).toBeInTheDocument();
  });

  it('shows an Alert with the error headline when isError', () => {
    render(
      <QueryBoundary isLoading={false} isError error={new Error('boom')} loadingLabel="Loading…">
        <p>content</p>
      </QueryBoundary>,
    );
    expect(screen.getByText('Something went wrong.')).toBeInTheDocument();
  });

  it('renders children regardless of loading/error state (children own their own data-presence checks)', () => {
    render(
      <QueryBoundary isLoading isError loadingLabel="Loading…">
        <p>content</p>
      </QueryBoundary>,
    );
    expect(screen.getByText('content')).toBeInTheDocument();
  });

  it('shows neither Loader nor Alert when isLoading and isError are both false', () => {
    render(
      <QueryBoundary isLoading={false} isError={false} loadingLabel="Loading…">
        <p>content</p>
      </QueryBoundary>,
    );
    expect(screen.queryByText('Loading…')).not.toBeInTheDocument();
    expect(screen.queryByText('Something went wrong.')).not.toBeInTheDocument();
  });
});
