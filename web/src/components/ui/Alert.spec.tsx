import { describe, it, expect } from 'vitest';
import { render, screen } from '@testing-library/react';
import { Alert } from './Alert';

describe('Alert', () => {
  it('renders its children as the main message', () => {
    render(<Alert>Something went wrong.</Alert>);
    expect(screen.getByText('Something went wrong.')).toBeInTheDocument();
  });

  it('renders detail text in a separate block when provided', () => {
    render(<Alert detail="raw stack trace here">Could not save.</Alert>);
    expect(screen.getByText('raw stack trace here')).toBeInTheDocument();
  });

  it('does not render a detail block when detail is omitted', () => {
    const { container } = render(<Alert>No detail here.</Alert>);
    expect(container.querySelector('pre')).toBeNull();
  });

  it('applies the danger variant styling by default', () => {
    render(<Alert>Error</Alert>);
    const alert = screen.getByText('Error').closest('div');
    expect(alert?.className).toContain('danger');
  });
});
