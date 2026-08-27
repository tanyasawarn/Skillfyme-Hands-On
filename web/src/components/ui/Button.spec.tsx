import { describe, it, expect, vi } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/react';
import { Button } from './Button';

describe('Button', () => {
  it('renders its children', () => {
    render(<Button>Start attempt</Button>);
    expect(screen.getByRole('button', { name: 'Start attempt' })).toBeInTheDocument();
  });

  it('calls onClick when clicked', () => {
    const onClick = vi.fn();
    render(<Button onClick={onClick}>Click me</Button>);
    fireEvent.click(screen.getByRole('button'));
    expect(onClick).toHaveBeenCalledTimes(1);
  });

  it('does not call onClick when disabled', () => {
    const onClick = vi.fn();
    render(
      <Button onClick={onClick} disabled>
        Click me
      </Button>,
    );
    fireEvent.click(screen.getByRole('button'));
    expect(onClick).not.toHaveBeenCalled();
  });

  it('defaults to the primary variant class', () => {
    render(<Button>Default</Button>);
    expect(screen.getByRole('button')).toHaveClass('lms-action-btn', 'lms-action-btn--primary');
  });

  it('applies the outline variant class when requested', () => {
    render(<Button variant="outline">Outline</Button>);
    expect(screen.getByRole('button')).toHaveClass('lms-action-btn--outline');
    expect(screen.getByRole('button')).not.toHaveClass('lms-action-btn--primary');
  });

  it('applies the sm size class when requested', () => {
    render(<Button size="sm">Small</Button>);
    expect(screen.getByRole('button')).toHaveClass('lms-action-btn--sm');
  });

  it('merges a caller-supplied className alongside the base classes', () => {
    render(<Button className="mt-10 w-full">Custom</Button>);
    const btn = screen.getByRole('button');
    expect(btn).toHaveClass('lms-action-btn', 'mt-10', 'w-full');
  });
});
