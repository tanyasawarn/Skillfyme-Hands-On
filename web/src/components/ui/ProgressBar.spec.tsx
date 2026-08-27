import { describe, it, expect } from 'vitest';
import { render } from '@testing-library/react';
import { ProgressBar } from './ProgressBar';

describe('ProgressBar', () => {
  it('renders the track with the shared track classes', () => {
    const { container } = render(<ProgressBar value={0.5} fillClassName="bg-[var(--accent)]" />);
    const track = container.firstChild as HTMLElement;
    expect(track.className).toContain('h-1.5');
    expect(track.className).toContain('rounded-full');
    expect(track.className).toContain('bg-[var(--inset)]');
  });

  it('sets the inner fill width from a 0-1 value as a percentage', () => {
    const { container } = render(<ProgressBar value={0.73} fillClassName="bg-[var(--accent)]" />);
    const fill = container.querySelector(':scope > div > div') as HTMLElement;
    expect(fill.style.width).toBe('73%');
  });

  it('applies the given fillClassName to the inner bar', () => {
    const { container } = render(<ProgressBar value={0.5} fillClassName="bg-[var(--success)]" />);
    const fill = container.querySelector(':scope > div > div') as HTMLElement;
    expect(fill.className).toContain('bg-[var(--success)]');
  });

  it('does not add rounded-full/transition to the fill when animated is omitted (mastery bar)', () => {
    const { container } = render(<ProgressBar value={0.5} fillClassName="bg-[var(--accent)]" />);
    const fill = container.querySelector(':scope > div > div') as HTMLElement;
    expect(fill.className).not.toContain('transition-[width]');
  });

  it('adds rounded-full + transition to the fill when animated (criteria bar)', () => {
    const { container } = render(<ProgressBar value={0.5} fillClassName="bg-[var(--accent)]" animated />);
    const fill = container.querySelector(':scope > div > div') as HTMLElement;
    expect(fill.className).toContain('rounded-full');
    expect(fill.className).toContain('transition-[width]');
  });
});
