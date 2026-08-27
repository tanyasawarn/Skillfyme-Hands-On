import { describe, it, expect } from 'vitest';
import { render, screen } from '@testing-library/react';
import { CardLink } from './CardLink';

describe('CardLink', () => {
  it('renders as a real anchor (Next.js Link) pointing at href by default', () => {
    render(<CardLink variant="plain" href="/catalog/abc">content</CardLink>);
    const link = screen.getByRole('link');
    expect(link).toHaveAttribute('href', '/catalog/abc');
  });

  it('applies the lift variant classes (SkillCard live)', () => {
    render(<CardLink variant="lift" href="/x">content</CardLink>);
    const link = screen.getByRole('link');
    expect(link.className).toContain('skill-card');
    expect(link.className).toContain('skill-card--live');
  });

  it('applies the row variant classes (HistoryRow)', () => {
    render(<CardLink variant="row" href="/x">content</CardLink>);
    const link = screen.getByRole('link');
    expect(link.className).toContain('rounded-lg');
    expect(link.className).toContain('hover:border-[var(--accent)]');
  });

  it('applies the plain variant classes (home dashboard cards)', () => {
    render(<CardLink variant="plain" href="/x">content</CardLink>);
    const link = screen.getByRole('link');
    expect(link.className).toContain('lms-card');
  });

  it('renders as a non-interactive div when disabled, not a link', () => {
    render(<CardLink variant="lift" href="/x" disabled>content</CardLink>);
    expect(screen.queryByRole('link')).not.toBeInTheDocument();
    expect(screen.getByText('content')).toBeInTheDocument();
  });

  it('uses the locked chrome class when disabled on the lift variant', () => {
    render(<CardLink variant="lift" href="/x" disabled>content</CardLink>);
    expect(screen.getByText('content').className).toContain('skill-card--locked');
  });

  it('merges an additional className', () => {
    render(<CardLink variant="plain" href="/x" className="extra">content</CardLink>);
    expect(screen.getByRole('link').className).toContain('extra');
  });

  it('renders arbitrary children, not a fixed content shape', () => {
    render(
      <CardLink variant="row" href="/x">
        <span>left</span>
        <span>right</span>
      </CardLink>,
    );
    expect(screen.getByText('left')).toBeInTheDocument();
    expect(screen.getByText('right')).toBeInTheDocument();
  });
});
