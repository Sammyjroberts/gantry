// @gantry/ui — placeholder shared UI-primitives package.
// Intentionally minimal for now; console-specific components live in apps/web
// until enough is shared to promote here.

/** Design tokens shared by Gantry consoles (dark engineering theme). */
export const tokens = {
  bg: "#0b0e11",
  panel: "#12161b",
  border: "#232a31",
  text: "#c7d0d9",
  muted: "#7d8892",
  accent: "#4fd1c5",
  warn: "#e2b93b",
  danger: "#e5484d",
  ok: "#3fb950",
  mono: '"JetBrains Mono", "SF Mono", "Cascadia Code", Consolas, monospace',
} as const;

export type Tokens = typeof tokens;
