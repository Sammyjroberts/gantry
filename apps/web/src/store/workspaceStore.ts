/**
 * Workspace editing + selection store.
 *
 * Holds the client-side editing state for the active workspace: the panel list
 * (the grid model), its id/name, a dirty flag that drives debounced autosave,
 * and the channel-sidebar selection that feeds the "add as chart" flows. Server
 * state (the list of workspaces, load/save) lives in the TanStack Query layer;
 * this store is the local, mutable working copy the grid edits in place.
 *
 * Panel mutations bump `dirty`; the persistence hook watches (panels, name,
 * dirty) and Upserts on a ~2s debounce, calling {@link markSaved} on success.
 */

import { create } from "zustand";
import {
  makePanel,
  type Panel,
  type PanelConfig,
  type PanelType,
  type GridPos,
} from "../workspace/layout";

export interface WorkspaceState {
  // ---- active workspace working copy ----
  currentId: string | null;
  name: string;
  panels: Panel[];
  /** Unsaved edits pending an autosave Upsert. */
  dirty: boolean;
  /** True while the grid is in edit mode (drag/resize/remove affordances). */
  editing: boolean;

  // ---- channel sidebar selection (add-as-chart flows) ----
  selection: Set<string>;

  // ---- workspace lifecycle ----
  /** Replace the working copy from a loaded (or freshly seeded) workspace. */
  loadWorkspace: (args: { id: string | null; name: string; panels: Panel[] }) => void;
  setName: (name: string) => void;
  markSaved: (id?: string) => void;
  setEditing: (editing: boolean) => void;

  // ---- panel mutations (all bump dirty) ----
  setPanels: (panels: Panel[]) => void;
  addPanel: (type: PanelType, config?: PanelConfig) => Panel;
  addPanelInstance: (panel: Panel) => void;
  removePanel: (id: string) => void;
  updateConfig: (id: string, config: PanelConfig) => void;
  setPanelTitle: (id: string, title: string | undefined) => void;
  /** Apply react-grid-layout positions back onto the panels (bumps dirty). */
  applyGrid: (positions: Record<string, GridPos>) => void;

  // ---- selection ----
  setSelection: (keys: Set<string>) => void;
  toggleSelection: (key: string) => void;
  clearSelection: () => void;
}

/** Next free row for a newly-added panel (stacks under the tallest column). */
function nextRow(panels: Panel[]): number {
  let bottom = 0;
  for (const p of panels) bottom = Math.max(bottom, p.grid.y + p.grid.h);
  return bottom;
}

export const useWorkspaceStore = create<WorkspaceState>((set, get) => ({
  currentId: null,
  name: "",
  panels: [],
  dirty: false,
  editing: false,
  selection: new Set<string>(),

  loadWorkspace: ({ id, name, panels }) =>
    set({ currentId: id, name, panels, dirty: false }),
  setName: (name) => set({ name, dirty: true }),
  markSaved: (id) => set((s) => ({ dirty: false, currentId: id ?? s.currentId })),
  setEditing: (editing) => set({ editing }),

  setPanels: (panels) => set({ panels, dirty: true }),
  addPanel: (type, config) => {
    const panel = makePanel(type, 0, nextRow(get().panels), config);
    set((s) => ({ panels: [...s.panels, panel], dirty: true }));
    return panel;
  },
  addPanelInstance: (panel) =>
    set((s) => ({ panels: [...s.panels, panel], dirty: true })),
  removePanel: (id) =>
    set((s) => ({ panels: s.panels.filter((p) => p.id !== id), dirty: true })),
  updateConfig: (id, config) =>
    set((s) => ({
      panels: s.panels.map((p) => (p.id === id ? { ...p, config } : p)),
      dirty: true,
    })),
  setPanelTitle: (id, title) =>
    set((s) => ({
      panels: s.panels.map((p) => (p.id === id ? { ...p, title } : p)),
      dirty: true,
    })),
  applyGrid: (positions) =>
    set((s) => {
      let changed = false;
      const panels = s.panels.map((p) => {
        const g = positions[p.id];
        if (!g) return p;
        if (g.x === p.grid.x && g.y === p.grid.y && g.w === p.grid.w && g.h === p.grid.h) {
          return p;
        }
        changed = true;
        return { ...p, grid: { x: g.x, y: g.y, w: g.w, h: g.h } };
      });
      return changed ? { panels, dirty: true } : {};
    }),

  setSelection: (keys) => set({ selection: new Set(keys) }),
  toggleSelection: (key) =>
    set((s) => {
      const next = new Set(s.selection);
      if (next.has(key)) next.delete(key);
      else next.add(key);
      return { selection: next };
    }),
  clearSelection: () => set({ selection: new Set<string>() }),
}));

/** localStorage key for the last-opened workspace id (ephemeral view state). */
export const LAST_WORKSPACE_KEY = "gantry-last-workspace";

export function readLastWorkspaceId(): string | null {
  if (typeof localStorage === "undefined") return null;
  try {
    return localStorage.getItem(LAST_WORKSPACE_KEY);
  } catch {
    return null;
  }
}
export function writeLastWorkspaceId(id: string | null): void {
  if (typeof localStorage === "undefined") return;
  try {
    if (id) localStorage.setItem(LAST_WORKSPACE_KEY, id);
    else localStorage.removeItem(LAST_WORKSPACE_KEY);
  } catch {
    /* disabled storage — non-fatal */
  }
}
