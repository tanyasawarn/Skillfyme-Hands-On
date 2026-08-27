import { describe, it, expect } from 'vitest';
import { render, screen } from '@testing-library/react';
import { Badge } from './Badge';

describe('Badge', () => {
  it('renders as a pill by default, with the lms-pill classes for the given variant', () => {
    render(<Badge variant="success">Passed</Badge>);
    const el = screen.getByText('Passed');
    expect(el.className).toContain('lms-pill');
    expect(el.className).toContain('lms-pill--success');
  });

  it('renders an icon before the label when icon is provided, and applies the icon-gap class', () => {
    render(<Badge variant="success" icon={<svg data-testid="icon" />}>Live</Badge>);
    expect(screen.getByTestId('icon')).toBeInTheDocument();
    expect(screen.getByText('Live').className).toContain('lms-pill--with-icon');
  });

  it('does not apply the icon-gap class when no icon is given', () => {
    render(<Badge variant="muted">Locked</Badge>);
    expect(screen.getByText('Locked').className).not.toContain('lms-pill--with-icon');
  });

  it('supports the outline variant (skill-card-badge--locked equivalent)', () => {
    render(<Badge variant="outline">Locked</Badge>);
    expect(screen.getByText('Locked').className).toContain('lms-pill--outline');
  });

  it('renders as a small circle when shape="circle", with no pill classes', () => {
    render(<Badge shape="circle" variant="danger">✕</Badge>);
    const el = screen.getByText('✕');
    expect(el.className).toContain('rounded-full');
    expect(el.className).not.toContain('lms-pill');
  });

  it('merges an additional className onto the pill', () => {
    render(<Badge variant="accent" className="extra-class">X</Badge>);
    expect(screen.getByText('X').className).toContain('extra-class');
  });
});
