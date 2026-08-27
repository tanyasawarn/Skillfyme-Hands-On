import { describe, it, expect } from 'vitest';
import { render, screen } from '@testing-library/react';
import { SectionLabel } from './SectionLabel';

describe('SectionLabel (PLAN.md C6)', () => {
  it('defaults to an h2 element with the font-mono-label class', () => {
    render(<SectionLabel>Tasks</SectionLabel>);
    const el = screen.getByText('Tasks');
    expect(el.tagName).toBe('H2');
    expect(el.className).toContain('font-mono-label');
  });

  it.each(['h2', 'h3', 'p', 'span', 'label'] as const)('renders as=%s using the matching real DOM tag', (as) => {
    render(<SectionLabel as={as}>Label text</SectionLabel>);
    const el = screen.getByText('Label text');
    expect(el.tagName).toBe(as.toUpperCase());
  });

  it('merges an additional className alongside font-mono-label', () => {
    render(<SectionLabel className="mt-0.5">Domain</SectionLabel>);
    const el = screen.getByText('Domain');
    expect(el.className).toContain('font-mono-label');
    expect(el.className).toContain('mt-0.5');
  });

  it('passes htmlFor only when rendering as a label', () => {
    render(
      <SectionLabel as="label" htmlFor="course-switcher">
        Course
      </SectionLabel>,
    );
    const el = screen.getByText('Course');
    expect(el.getAttribute('for')).toBe('course-switcher');
  });

  it('does not leak htmlFor onto a non-label element', () => {
    render(
      <SectionLabel as="span" htmlFor="should-not-appear">
        Score
      </SectionLabel>,
    );
    const el = screen.getByText('Score');
    expect(el.getAttribute('for')).toBeNull();
  });
});
