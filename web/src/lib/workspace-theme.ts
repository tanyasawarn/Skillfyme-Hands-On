/**
 * PLAN.md T3: workspace terminal dark-theme colors. xterm.js's `theme`
 * option needs real hex strings (it doesn't resolve CSS custom
 * properties), so this is the JS-side counterpart to globals.css's
 * `--workspace-bg`/`--workspace-fg` tokens -- both must be kept in sync
 * by hand since a JS constant and a CSS custom property can't share one
 * literal, but this is the one place either needs editing, replacing
 * the previous two independent hardcoded occurrences within
 * WorkspaceTerminal.tsx itself.
 */
export const WORKSPACE_BG = '#0a0a0a';
export const WORKSPACE_FG = '#e5e5e5';
