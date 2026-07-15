import { useExperiments } from "../useExperiments";
import { useCatalog } from "../query/useCatalog";
import { resolveBaseUrl } from "../config";
import { useMemo } from "react";
import { WorkspaceDataProvider } from "../live/WorkspaceData";
import { WorkspaceToolbar } from "../components/WorkspaceToolbar";
import { WorkspaceSidebar } from "../components/WorkspaceSidebar";
import { TimeStrips } from "../components/TimeStrips";
import { WorkspaceGrid } from "../panels/WorkspaceGrid";
import { useWorkspaceManager } from "../hooks/useWorkspaceManager";

/**
 * The Workspace page — the bench builder. A toolbar (workspace switcher + add
 * panel + edit mode), the global time/replay/experiment strips, a collapsible
 * channel sidebar, and the panel grid.
 */
export function WorkspacePage() {
  const baseUrl = useMemo(resolveBaseUrl, []);
  const catalog = useCatalog();
  const mgr = useWorkspaceManager(catalog);
  const exp = useExperiments({ baseUrl, deviceId: "", pollMs: 5000 });

  return (
    <WorkspaceDataProvider experiments={exp.experiments}>
      <div className="workspace-page">
        <WorkspaceToolbar mgr={mgr} />
        <TimeStrips exp={exp} />
        <div className="ws-main">
          <WorkspaceSidebar />
          <div className="ws-grid-area">
            {!mgr.ready ? (
              <div className="workspace-empty">loading workspace…</div>
            ) : (
              <WorkspaceGrid />
            )}
          </div>
        </div>
      </div>
    </WorkspaceDataProvider>
  );
}
