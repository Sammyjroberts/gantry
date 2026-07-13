import { useEffect, useMemo, useRef, useState } from "react";
import type { Experiment } from "@gantry/api-client";
import {
  defaultExperimentName,
  durationSec,
  formatDuration,
  isRunning,
  experimentCsvHref,
} from "../experiments";

export interface ExperimentBarProps {
  experiments: Experiment[];
  running: Experiment[];
  error: string | null;
  baseUrl: string;
  /** Distinct selected channel NAMES, for the "selected" export scope. */
  exportChannels: string[];
  onStart: (name: string) => void;
  onStop: (id: string) => void;
  onUpdate: (id: string, name: string, notes: string) => void;
  onDelete: (id: string) => void;
  /** Zoom all charts to an experiment's [start, end] (epoch seconds). */
  onZoomTo: (startSec: number, endSec: number) => void;
  /** Enter replay for an experiment's [start, end] (epoch seconds). */
  onReplay: (id: string, startSec: number, endSec: number) => void;
}

type CsvFormat = "long" | "wide";
type CsvScope = "selected" | "all";

interface EditState {
  id: string;
  field: "name" | "notes";
  value: string;
}

/**
 * Experiment (test-run) control bar + collapsible history panel.
 *
 * The bar itself is always visible: a name input + "Start test" while idle; a
 * live elapsed timer, recording dot and "Stop" for the PRIMARY running run
 * (newest). Any additional concurrent runs appear as compact stop-chips — the
 * primary UX stays single-active while still allowing concurrency.
 *
 * The panel lists every run newest-first with inline-editable name/notes
 * (committed via UpdateExperiment on Enter/blur), a per-row CSV export honoring
 * the panel's format/scope toggles, delete-with-confirm, and a click-to-zoom
 * affordance that drives every chart to the run's time range.
 */
export function ExperimentBar({
  experiments,
  running,
  error,
  baseUrl,
  exportChannels,
  onStart,
  onStop,
  onUpdate,
  onDelete,
  onZoomTo,
  onReplay,
}: ExperimentBarProps) {
  const [name, setName] = useState("");
  const [open, setOpen] = useState(false);
  const [format, setFormat] = useState<CsvFormat>("long");
  const [scope, setScope] = useState<CsvScope>("all");
  const [edit, setEdit] = useState<EditState | null>(null);
  const [confirmDelete, setConfirmDelete] = useState<string | null>(null);

  // Own 1s tick for the elapsed timer / live durations, independent of the
  // chart pause so the recording clock never freezes.
  const [nowMs, setNowMs] = useState(() => Date.now());
  useEffect(() => {
    const id = setInterval(() => setNowMs(Date.now()), 1000);
    return () => clearInterval(id);
  }, []);
  const nowNs = BigInt(nowMs) * 1_000_000n;

  // Regenerate the placeholder each render so it tracks the wall clock until the
  // operator types their own name.
  const placeholder = defaultExperimentName(new Date(nowMs));

  const primary = running[0] ?? null; // newest running = the focused run
  const others = running.slice(1);

  const submit = () => {
    onStart(name.trim() || placeholder);
    setName("");
  };

  const channels = scope === "selected" ? exportChannels : [];

  const commitEdit = () => {
    if (!edit) return;
    const exp = experiments.find((e) => e.id === edit.id);
    if (exp) {
      const nextName = edit.field === "name" ? edit.value.trim() || exp.name : exp.name;
      const nextNotes = edit.field === "notes" ? edit.value : exp.notes;
      if (nextName !== exp.name || nextNotes !== exp.notes) {
        onUpdate(exp.id, nextName, nextNotes);
      }
    }
    setEdit(null);
  };

  const sorted = useMemo(() => experiments, [experiments]);

  return (
    <div className="exp-bar">
      <div className="exp-controls">
        {primary ? (
          <div className="exp-active">
            <span className="exp-rec-dot" />
            <span className="exp-active-name" title={primary.notes || primary.name}>
              {primary.name}
            </span>
            <span className="exp-timer">{formatDuration(durationSec(primary, nowNs))}</span>
            <button className="exp-stop" onClick={() => onStop(primary.id)}>
              ■ Stop
            </button>
          </div>
        ) : (
          <div className="exp-start">
            <StartInput
              value={name}
              placeholder={placeholder}
              onChange={setName}
              onSubmit={submit}
            />
            <button className="exp-start-btn" onClick={submit}>
              ● Start test
            </button>
          </div>
        )}

        {others.length > 0 && (
          <div className="exp-others">
            {others.map((e) => (
              <button
                key={e.id}
                className="exp-chip"
                title={`${e.name} — ${formatDuration(durationSec(e, nowNs))} · click to stop`}
                onClick={() => onStop(e.id)}
              >
                <span className="exp-rec-dot exp-rec-dot--sm" />
                {e.name} ■
              </button>
            ))}
          </div>
        )}

        <button
          className={`exp-toggle ${open ? "is-open" : ""}`}
          onClick={() => setOpen((v) => !v)}
          aria-expanded={open}
        >
          {open ? "▾" : "▸"} experiments
          <span className="exp-count">{experiments.length}</span>
        </button>
      </div>

      {error && <div className="exp-error">experiments: {error}</div>}

      {open && (
        <div className="exp-panel">
          <div className="exp-panel-head">
            <span className="exp-panel-title">TEST RUNS</span>
            <label className="exp-opt">
              scope
              <select value={scope} onChange={(e) => setScope(e.target.value as CsvScope)}>
                <option value="all">all channels</option>
                <option value="selected">
                  selected ({exportChannels.length})
                </option>
              </select>
            </label>
            <label className="exp-opt">
              format
              <select value={format} onChange={(e) => setFormat(e.target.value as CsvFormat)}>
                <option value="long">long</option>
                <option value="wide">wide</option>
              </select>
            </label>
          </div>

          {sorted.length === 0 && <div className="exp-empty">no test runs yet</div>}

          <ul className="exp-list">
            {sorted.map((exp) => {
              const run = isRunning(exp);
              const startSec = Number(exp.startNs) / 1e9;
              const endSec = run ? nowMs / 1000 : Number(exp.endNs) / 1e9;
              const editingName = edit?.id === exp.id && edit.field === "name";
              const editingNotes = edit?.id === exp.id && edit.field === "notes";
              return (
                <li className="exp-row" key={exp.id}>
                  <div className="exp-row-main">
                    <button
                      className="exp-zoom"
                      title="view entire run"
                      onClick={() => onZoomTo(startSec, endSec)}
                    >
                      ⤢
                    </button>
                    <button
                      className="exp-replay"
                      title="replay this run"
                      onClick={() => onReplay(exp.id, startSec, endSec)}
                    >
                      ▶
                    </button>
                    {editingName ? (
                      <input
                        className="exp-edit"
                        autoFocus
                        value={edit.value}
                        onChange={(e) =>
                          setEdit((prev) => (prev ? { ...prev, value: e.target.value } : prev))
                        }
                        onBlur={commitEdit}
                        onKeyDown={(e) => {
                          if (e.key === "Enter") commitEdit();
                          if (e.key === "Escape") setEdit(null);
                        }}
                      />
                    ) : (
                      <button
                        className="exp-name"
                        title="click to rename"
                        onClick={() => setEdit({ id: exp.id, field: "name", value: exp.name })}
                      >
                        {exp.name}
                      </button>
                    )}
                    <span className={`exp-badge ${run ? "is-running" : "is-done"}`}>
                      {run ? "running" : "done"}
                    </span>
                    <span className="exp-dur">{formatDuration(durationSec(exp, nowNs))}</span>
                    <span className="exp-row-actions">
                      <a
                        className="exp-act"
                        href={experimentCsvHref(baseUrl, exp.id, channels, format)}
                        download
                        title={`export CSV (${scope}, ${format})`}
                      >
                        ⤓ csv
                      </a>
                      {run && (
                        <button className="exp-act" onClick={() => onStop(exp.id)}>
                          ■ stop
                        </button>
                      )}
                      {confirmDelete === exp.id ? (
                        <>
                          <button
                            className="exp-act exp-act--danger"
                            onClick={() => {
                              onDelete(exp.id);
                              setConfirmDelete(null);
                            }}
                          >
                            confirm
                          </button>
                          <button className="exp-act" onClick={() => setConfirmDelete(null)}>
                            cancel
                          </button>
                        </>
                      ) : (
                        <button
                          className="exp-act exp-act--danger"
                          onClick={() => setConfirmDelete(exp.id)}
                        >
                          ✕ del
                        </button>
                      )}
                    </span>
                  </div>
                  <div className="exp-row-notes">
                    {editingNotes ? (
                      <input
                        className="exp-edit exp-edit--notes"
                        autoFocus
                        value={edit.value}
                        placeholder="add notes…"
                        onChange={(e) =>
                          setEdit((prev) => (prev ? { ...prev, value: e.target.value } : prev))
                        }
                        onBlur={commitEdit}
                        onKeyDown={(e) => {
                          if (e.key === "Enter") commitEdit();
                          if (e.key === "Escape") setEdit(null);
                        }}
                      />
                    ) : (
                      <button
                        className={`exp-notes ${exp.notes ? "" : "is-empty"}`}
                        title="click to edit notes"
                        onClick={() => setEdit({ id: exp.id, field: "notes", value: exp.notes })}
                      >
                        {exp.notes || "add notes…"}
                      </button>
                    )}
                  </div>
                </li>
              );
            })}
          </ul>
        </div>
      )}
    </div>
  );
}

interface StartInputProps {
  value: string;
  placeholder: string;
  onChange: (v: string) => void;
  onSubmit: () => void;
}

/** Name input: autofocus on mount, Enter starts the run. */
function StartInput({ value, placeholder, onChange, onSubmit }: StartInputProps) {
  const ref = useRef<HTMLInputElement>(null);
  useEffect(() => {
    ref.current?.focus();
  }, []);
  return (
    <input
      ref={ref}
      className="exp-name-input"
      value={value}
      placeholder={placeholder}
      onChange={(e) => onChange(e.target.value)}
      onKeyDown={(e) => {
        if (e.key === "Enter") onSubmit();
      }}
    />
  );
}
