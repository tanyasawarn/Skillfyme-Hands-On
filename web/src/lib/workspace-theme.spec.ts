import { describe, it, expect } from 'vitest';
import { WORKSPACE_BG, WORKSPACE_FG } from './workspace-theme';

describe('workspace-theme (PLAN.md T3)', () => {
  it('matches the previously-duplicated xterm theme literals exactly', () => {
    expect(WORKSPACE_BG).toBe('#0a0a0a');
    expect(WORKSPACE_FG).toBe('#e5e5e5');
  });
});
