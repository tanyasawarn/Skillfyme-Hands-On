import { describe, it, expect } from 'vitest';
import { render, screen } from '@testing-library/react';
import { PageContainer } from './PageContainer';

describe('PageContainer', () => {
  it('defaults to max-w-3xl with mx-auto and px-6', () => {
    render(<PageContainer spacing="py-10">content</PageContainer>);
    const el = screen.getByText('content');
    expect(el.className).toContain('mx-auto');
    expect(el.className).toContain('max-w-3xl');
    expect(el.className).toContain('px-6');
  });

  it('applies the requested vertical spacing utility verbatim', () => {
    render(<PageContainer spacing="py-12">content</PageContainer>);
    expect(screen.getByText('content').className).toContain('py-12');
  });

  it.each(['3xl', '4xl', '6xl'] as const)('supports maxWidth=%s', (maxWidth) => {
    render(<PageContainer maxWidth={maxWidth} spacing="py-10">content</PageContainer>);
    expect(screen.getByText('content').className).toContain(`max-w-${maxWidth}`);
  });

  it('merges an additional className', () => {
    render(<PageContainer spacing="py-10" className="extra">content</PageContainer>);
    expect(screen.getByText('content').className).toContain('extra');
  });
});
