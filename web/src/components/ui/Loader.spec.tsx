import { describe, it, expect } from 'vitest';
import { render, screen } from '@testing-library/react';
import { Loader } from './Loader';

describe('Loader', () => {
  it('renders the given label', () => {
    render(<Loader label="Loading catalog…" />);
    expect(screen.getByText('Loading catalog…')).toBeInTheDocument();
  });

  it('defaults to mt-8 spacing when no className is given', () => {
    render(<Loader label="Loading…" />);
    expect(screen.getByText('Loading…')).toHaveClass('mt-8');
  });

  it('fully replaces the default spacing when className is given (no mt-8/mt-10 class conflict)', () => {
    render(<Loader label="Loading…" className="mt-10" />);
    const el = screen.getByText('Loading…');
    expect(el).toHaveClass('mt-10');
    expect(el).not.toHaveClass('mt-8');
  });
});
