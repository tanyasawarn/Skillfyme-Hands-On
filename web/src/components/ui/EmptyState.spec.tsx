import { describe, it, expect } from 'vitest';
import { render, screen } from '@testing-library/react';
import { EmptyState } from './EmptyState';

describe('EmptyState', () => {
  it('renders its children as a message by default', () => {
    render(<EmptyState>No attempts yet.</EmptyState>);
    expect(screen.getByText('No attempts yet.')).toBeInTheDocument();
  });

  it('renders arbitrary JSX children, not just a plain string (history.tsx embeds a Link)', () => {
    render(
      <EmptyState>
        No attempts yet. Go to <a href="https://example.test/catalog">catalog</a>.
      </EmptyState>,
    );
    expect(screen.getByRole('link', { name: 'catalog' })).toBeInTheDocument();
  });

  it('applies the default mt-8 message styling when no className is given', () => {
    render(<EmptyState>Empty</EmptyState>);
    expect(screen.getByText('Empty').className).toBe('mt-8 text-[var(--ink-soft)]');
  });

  it('fully replaces the default className when one is given, not appends', () => {
    render(<EmptyState className="mt-2 text-sm text-[var(--ink-soft)]">Empty</EmptyState>);
    expect(screen.getByText('Empty').className).toBe('mt-2 text-sm text-[var(--ink-soft)]');
  });

  it('renders as a bordered card when variant="card"', () => {
    render(<EmptyState variant="card">No tasks for this activity.</EmptyState>);
    expect(screen.getByText('No tasks for this activity.').className).toContain('lms-card');
  });
});
