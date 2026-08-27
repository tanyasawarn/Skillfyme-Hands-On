import type { ButtonHTMLAttributes, ReactNode } from 'react';

/**
 * Phase 3 standardization: replaces the hand-rolled
 * `<button className="lms-action-btn lms-action-btn--primary">` pattern
 * duplicated across at least 4 call sites (catalog activity page,
 * attempt detail page x2, WorkspaceEditor's save button) -- the CSS
 * class system in globals.css (`.lms-action-btn` + modifiers) was
 * already solid and shared; the actual duplication was the element
 * itself and its prop wiring (disabled state, size, variant), not the
 * styling. This wraps that existing class system rather than
 * reinventing it.
 */
export type ButtonVariant = 'primary' | 'outline';
export type ButtonSize = 'default' | 'sm';

interface ButtonProps extends Omit<ButtonHTMLAttributes<HTMLButtonElement>, 'className'> {
  variant?: ButtonVariant;
  size?: ButtonSize;
  children: ReactNode;
  className?: string;
}

const VARIANT_CLASS: Record<ButtonVariant, string> = {
  primary: 'lms-action-btn--primary',
  outline: 'lms-action-btn--outline',
};

export function Button({
  variant = 'primary',
  size = 'default',
  children,
  className,
  ...rest
}: ButtonProps) {
  const classes = [
    'lms-action-btn',
    VARIANT_CLASS[variant],
    size === 'sm' ? 'lms-action-btn--sm' : '',
    className ?? '',
  ]
    .filter(Boolean)
    .join(' ');

  return (
    <button className={classes} {...rest}>
      {children}
    </button>
  );
}
