import { useState } from "react";
import type { Hardware } from "@gantry/api-client";
import type { HardwarePatch } from "./useHardware";

export interface HardwarePanelProps {
  hardware: Hardware[];
  unconfigured: string[];
  error: string | null;
  onUpsert: (patch: HardwarePatch) => Promise<Hardware | null>;
  onRemove: (deviceId: string) => Promise<void>;
  onClose: () => void;
}

interface EditForm {
  deviceId: string;
  displayName: string;
  description: string;
  notes: string;
}

function formFor(hw: Hardware): EditForm {
  return {
    deviceId: hw.deviceId,
    displayName: hw.displayName,
    description: hw.description,
    notes: hw.notes,
  };
}

/**
 * Hardware page: the operator-authored device registry.
 *
 * Two sections: the configured hardware (display name, device id, edit) and the
 * "seen but unconfigured" devices — telemetry devices with no row yet — each
 * promotable with one click. Editing a row opens an inline form for
 * display_name / description / notes, saved via a merged Upsert (the hook
 * preserves the opaque JSON configs it doesn't touch). Not lazy: this panel is
 * lightweight (no heavy deps), so it ships in the main bundle.
 */
export function HardwarePanel({
  hardware,
  unconfigured,
  error,
  onUpsert,
  onRemove,
  onClose,
}: HardwarePanelProps) {
  const [edit, setEdit] = useState<EditForm | null>(null);
  const [confirmDelete, setConfirmDelete] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const save = async () => {
    if (!edit) return;
    setBusy(true);
    await onUpsert({
      deviceId: edit.deviceId,
      displayName: edit.displayName.trim(),
      description: edit.description,
      notes: edit.notes,
    });
    setBusy(false);
    setEdit(null);
  };

  const promote = async (deviceId: string) => {
    setBusy(true);
    // Create a bare row (implicitly configures the device); the operator can
    // name it via Edit afterwards.
    await onUpsert({ deviceId });
    setBusy(false);
  };

  return (
    <div className="hw-panel">
      <div className="hw-head">
        <span className="hw-title">HARDWARE</span>
        <button className="hw-close" onClick={onClose} title="close hardware panel">
          ✕
        </button>
      </div>

      {error && <div className="hw-error">hardware: {error}</div>}

      <div className="hw-section">
        <div className="hw-section-title">
          configured <span className="hw-count">{hardware.length}</span>
        </div>
        {hardware.length === 0 && <div className="hw-empty">no configured hardware yet</div>}
        <ul className="hw-list">
          {hardware.map((hw) => {
            const editing = edit?.deviceId === hw.deviceId;
            return (
              <li className="hw-row" key={hw.deviceId}>
                {editing ? (
                  <div className="hw-edit">
                    <label className="hw-field">
                      <span className="hw-field-label">display name</span>
                      <input
                        className="hw-input"
                        autoFocus
                        value={edit.displayName}
                        placeholder={hw.deviceId}
                        onChange={(e) =>
                          setEdit((p) => (p ? { ...p, displayName: e.target.value } : p))
                        }
                      />
                    </label>
                    <label className="hw-field">
                      <span className="hw-field-label">description</span>
                      <input
                        className="hw-input"
                        value={edit.description}
                        onChange={(e) =>
                          setEdit((p) => (p ? { ...p, description: e.target.value } : p))
                        }
                      />
                    </label>
                    <label className="hw-field">
                      <span className="hw-field-label">notes</span>
                      <textarea
                        className="hw-input hw-textarea"
                        value={edit.notes}
                        onChange={(e) =>
                          setEdit((p) => (p ? { ...p, notes: e.target.value } : p))
                        }
                      />
                    </label>
                    <div className="hw-edit-actions">
                      <button className="hw-btn hw-btn--primary" disabled={busy} onClick={() => void save()}>
                        save
                      </button>
                      <button className="hw-btn" disabled={busy} onClick={() => setEdit(null)}>
                        cancel
                      </button>
                    </div>
                  </div>
                ) : (
                  <div className="hw-row-main">
                    <div className="hw-row-id">
                      <span className="hw-name">{hw.displayName || hw.deviceId}</span>
                      <span className="hw-devid" title="device id">{hw.deviceId}</span>
                      {hw.description && <span className="hw-desc">{hw.description}</span>}
                    </div>
                    <div className="hw-row-actions">
                      <button className="hw-btn" onClick={() => setEdit(formFor(hw))}>
                        edit
                      </button>
                      {confirmDelete === hw.deviceId ? (
                        <>
                          <button
                            className="hw-btn hw-btn--danger"
                            onClick={() => {
                              void onRemove(hw.deviceId);
                              setConfirmDelete(null);
                            }}
                          >
                            confirm
                          </button>
                          <button className="hw-btn" onClick={() => setConfirmDelete(null)}>
                            cancel
                          </button>
                        </>
                      ) : (
                        <button
                          className="hw-btn hw-btn--danger"
                          onClick={() => setConfirmDelete(hw.deviceId)}
                        >
                          ✕
                        </button>
                      )}
                    </div>
                  </div>
                )}
              </li>
            );
          })}
        </ul>
      </div>

      <div className="hw-section">
        <div className="hw-section-title">
          seen · unconfigured <span className="hw-count">{unconfigured.length}</span>
        </div>
        {unconfigured.length === 0 && (
          <div className="hw-empty">all seen devices are configured</div>
        )}
        <ul className="hw-list">
          {unconfigured.map((id) => (
            <li className="hw-row hw-row--unconfigured" key={id}>
              <div className="hw-row-main">
                <span className="hw-devid hw-devid--strong">{id}</span>
                <button className="hw-btn hw-btn--primary" disabled={busy} onClick={() => void promote(id)}>
                  + promote
                </button>
              </div>
            </li>
          ))}
        </ul>
      </div>
    </div>
  );
}
