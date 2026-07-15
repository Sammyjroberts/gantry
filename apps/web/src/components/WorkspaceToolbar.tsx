import { useRef, useState } from "react";
import { Plus, Pencil, Check, Copy, Trash2, Download, Upload } from "lucide-react";
import { useWorkspaceStore } from "../store/workspaceStore";
import { PANEL_MENU } from "../panels/registry";
import type { WorkspaceManager } from "../hooks/useWorkspaceManager";

/**
 * Workspace toolbar: the workspace switcher (+ create/rename/delete/duplicate/
 * export/import), the add-panel menu, an edit-mode toggle (drag/resize), and the
 * autosave status.
 */
export function WorkspaceToolbar({ mgr }: { mgr: WorkspaceManager }) {
  const editing = useWorkspaceStore((s) => s.editing);
  const setEditing = useWorkspaceStore((s) => s.setEditing);
  const addPanel = useWorkspaceStore((s) => s.addPanel);
  const [addOpen, setAddOpen] = useState(false);
  const [renaming, setRenaming] = useState(false);
  const fileRef = useRef<HTMLInputElement>(null);

  return (
    <div className="ws-toolbar">
      <div className="ws-toolbar-left">
        <select
          className="ws-switcher"
          value={mgr.currentId ?? ""}
          onChange={(e) => void mgr.switchTo(e.target.value)}
          data-testid="workspace-switcher"
          title="switch workspace"
        >
          {mgr.workspaces.length === 0 && <option value="">(no workspaces)</option>}
          {mgr.workspaces.map((w) => (
            <option key={w.id} value={w.id}>
              {w.name || "(unnamed)"}
            </option>
          ))}
        </select>

        {renaming ? (
          <input
            className="ws-rename"
            autoFocus
            defaultValue={mgr.name}
            onBlur={(e) => {
              mgr.rename(e.target.value.trim() || mgr.name);
              setRenaming(false);
            }}
            onKeyDown={(e) => {
              if (e.key === "Enter") (e.target as HTMLInputElement).blur();
              if (e.key === "Escape") setRenaming(false);
            }}
          />
        ) : (
          <button className="ws-icon-btn" title="rename" onClick={() => setRenaming(true)}>
            <Pencil size={14} />
          </button>
        )}

        <button
          className="ws-icon-btn"
          title="new workspace"
          data-testid="workspace-new"
          onClick={() => void mgr.create("workspace")}
        >
          <Plus size={14} />
        </button>
        <button className="ws-icon-btn" title="duplicate" onClick={() => void mgr.duplicate()}>
          <Copy size={14} />
        </button>
        <button
          className="ws-icon-btn ws-icon-danger"
          title="delete workspace"
          onClick={() => {
            if (mgr.currentId && confirm(`Delete workspace "${mgr.name}"?`)) {
              void mgr.remove(mgr.currentId);
            }
          }}
        >
          <Trash2 size={14} />
        </button>
        <button className="ws-icon-btn" title="export layout JSON" onClick={mgr.exportCurrent}>
          <Download size={14} />
        </button>
        <button className="ws-icon-btn" title="import layout JSON" onClick={() => fileRef.current?.click()}>
          <Upload size={14} />
        </button>
        <input
          ref={fileRef}
          type="file"
          accept="application/json,.json"
          style={{ display: "none" }}
          onChange={(e) => {
            const f = e.target.files?.[0];
            if (f) void mgr.importLayout(f);
            e.target.value = "";
          }}
        />
      </div>

      <div className="ws-toolbar-right">
        <span className={`ws-save ${mgr.dirty ? "is-dirty" : ""}`} data-testid="workspace-save-state">
          {mgr.saving ? "saving…" : mgr.dirty ? "unsaved" : "saved"}
        </span>

        <button
          className={`ws-edit-btn ${editing ? "is-active" : ""}`}
          onClick={() => setEditing(!editing)}
          data-testid="workspace-edit-toggle"
          title={editing ? "done editing (lock layout)" : "edit layout (drag / resize)"}
        >
          {editing ? <Check size={14} /> : <Pencil size={14} />}
          {editing ? "done" : "edit"}
        </button>

        <div className="ws-add">
          <button
            className="ws-add-btn"
            onClick={() => setAddOpen((v) => !v)}
            data-testid="add-panel-btn"
          >
            <Plus size={14} /> add panel
          </button>
          {addOpen && (
            <>
              <div className="ws-add-backdrop" onClick={() => setAddOpen(false)} />
              <div className="ws-add-menu">
                {PANEL_MENU.map(({ type, label, Icon }) => (
                  <button
                    key={type}
                    className="ws-add-item"
                    data-testid={`add-panel-${type}`}
                    onClick={() => {
                      addPanel(type);
                      setAddOpen(false);
                      if (!editing) setEditing(true);
                    }}
                  >
                    <Icon size={14} /> {label}
                  </button>
                ))}
              </div>
            </>
          )}
        </div>
      </div>
    </div>
  );
}
