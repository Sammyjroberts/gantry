import { useState } from "react";
import { Settings, X } from "lucide-react";
import type { Panel, PanelConfig } from "../workspace/layout";
import { useWorkspaceStore } from "../store/workspaceStore";
import { PANEL_REGISTRY } from "./registry";
import { autoTitle } from "./common";

/**
 * Panel chrome: a draggable title bar (the react-grid-layout drag handle), a
 * gear that opens the per-type config popover (plus a custom-title override),
 * and a remove button. The body is the registered renderer for the panel type.
 */
export function PanelFrame({ panel, editing }: { panel: Panel; editing: boolean }) {
  const def = PANEL_REGISTRY[panel.type];
  const removePanel = useWorkspaceStore((s) => s.removePanel);
  const updateConfig = useWorkspaceStore((s) => s.updateConfig);
  const setPanelTitle = useWorkspaceStore((s) => s.setPanelTitle);
  const [showConfig, setShowConfig] = useState(false);

  const { Icon, Body, ConfigEditor } = def;
  const title = panel.title ?? autoTitle(panel);

  return (
    <div className={`panel ${editing ? "is-editing" : ""}`} data-panel-type={panel.type}>
      <div className="panel-head panel-drag">
        <span className="panel-head-title">
          <Icon size={13} className="panel-head-icon" aria-hidden />
          <span className="panel-title-text" title={title}>
            {title}
          </span>
        </span>
        <span className="panel-head-actions">
          <button
            className={`panel-btn ${showConfig ? "is-active" : ""}`}
            title="configure panel"
            onClick={() => setShowConfig((v) => !v)}
            onMouseDown={(e) => e.stopPropagation()}
          >
            <Settings size={13} />
          </button>
          {editing && (
            <button
              className="panel-btn panel-btn-danger"
              title="remove panel"
              onClick={() => removePanel(panel.id)}
              onMouseDown={(e) => e.stopPropagation()}
            >
              <X size={13} />
            </button>
          )}
        </span>
      </div>

      {showConfig && (
        <div className="panel-config-pop" onMouseDown={(e) => e.stopPropagation()}>
          <div className="panel-cfg">
            <div className="panel-cfg-label">title</div>
            <input
              className="panel-cfg-input"
              placeholder={autoTitle(panel)}
              value={panel.title ?? ""}
              onChange={(e) => setPanelTitle(panel.id, e.target.value || undefined)}
            />
          </div>
          {ConfigEditor && (
            <ConfigEditor
              panel={panel}
              onChange={(config: PanelConfig) => updateConfig(panel.id, config)}
            />
          )}
        </div>
      )}

      <div className="panel-body">
        <Body panel={panel} />
      </div>
    </div>
  );
}
