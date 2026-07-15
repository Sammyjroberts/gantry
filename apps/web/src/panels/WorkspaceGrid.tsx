import { useMemo } from "react";
import GridLayout, { WidthProvider, type Layout } from "react-grid-layout";
import { useWorkspaceStore } from "../store/workspaceStore";
import { PanelFrame } from "./PanelFrame";
import type { GridPos } from "../workspace/layout";

const Grid = WidthProvider(GridLayout);

export const GRID_COLS = 12;
export const GRID_ROW_HEIGHT = 40;

/**
 * The workspace panel grid. 12-column, vertically compacting react-grid-layout.
 * Drag (via each panel's title bar) and resize are gated on edit mode; layout
 * changes flow back into the workspace store (which bumps the dirty flag and
 * drives autosave). Panels render through {@link PanelFrame}.
 */
export function WorkspaceGrid() {
  const panels = useWorkspaceStore((s) => s.panels);
  const editing = useWorkspaceStore((s) => s.editing);
  const applyGrid = useWorkspaceStore((s) => s.applyGrid);

  const layout = useMemo<Layout[]>(
    () => panels.map((p) => ({ i: p.id, x: p.grid.x, y: p.grid.y, w: p.grid.w, h: p.grid.h, minW: 1, minH: 2 })),
    [panels],
  );

  if (panels.length === 0) {
    return (
      <div className="workspace-empty">
        This workspace has no panels yet. Use <strong>+ Add panel</strong> on the
        toolbar, or select channels in the sidebar and add them as a chart.
      </div>
    );
  }

  return (
    <Grid
      className="workspace-grid"
      layout={layout}
      cols={GRID_COLS}
      rowHeight={GRID_ROW_HEIGHT}
      margin={[10, 10]}
      containerPadding={[12, 12]}
      isDraggable={editing}
      isResizable={editing}
      draggableHandle=".panel-drag"
      compactType="vertical"
      onLayoutChange={(l: Layout[]) => {
        const positions: Record<string, GridPos> = {};
        for (const item of l) positions[item.i] = { x: item.x, y: item.y, w: item.w, h: item.h };
        applyGrid(positions);
      }}
    >
      {panels.map((p) => (
        <div key={p.id} className="grid-cell">
          <PanelFrame panel={p} editing={editing} />
        </div>
      ))}
    </Grid>
  );
}
