import { useState } from "react";
import { Plus, Trash2, X, Radio } from "lucide-react";
import type { Source, SourceStatus } from "@gantry/api-client";
import { useSourceList, useUpsertSource, useDeleteSource } from "../query/useSources";

/**
 * The "Telemetry sources" card on the Hardware page. Sources are connections the
 * Bench maintains to pull telemetry in from external publishers — a Foxglove
 * WebSocket server today (lerobot --display_mode=foxglove). Each row shows its
 * name/url, a live status dot, an ingested-frames counter, an ENABLED checkbox
 * that upserts immediately (starting/stopping the in-process client), and a
 * delete. The add form fixes type=foxglove and offers the built-in lerobot
 * profile or a custom mapping JSON document.
 */

const DEFAULT_URL = "ws://127.0.0.1:8765";
const LEROBOT_MAPPING = `{"profile":"lerobot"}`;

/** Human labels for the supervisor's state machine (source.proto SourceStatus). */
const STATE_LABEL: Record<string, string> = {
  connected: "connected",
  connecting: "connecting",
  backoff: "reconnecting",
  disabled: "disabled",
};

function stateOf(status: SourceStatus | undefined): string {
  return status?.state || "disabled";
}

function StatusDot({ status }: { status: SourceStatus | undefined }) {
  const state = stateOf(status);
  const label = STATE_LABEL[state] ?? state;
  const tip = status?.detail ? `${label} — ${status.detail}` : label;
  return (
    <span
      className={`source-dot source-dot--${state}`}
      title={tip}
      data-testid="source-dot"
      data-state={state}
      aria-label={tip}
    />
  );
}

export function SourcesCard() {
  const list = useSourceList();
  const upsert = useUpsertSource();
  const del = useDeleteSource();

  const [adding, setAdding] = useState(false);
  const [name, setName] = useState("");
  const [url, setUrl] = useState(DEFAULT_URL);
  const [profile, setProfile] = useState<"lerobot" | "custom">("lerobot");
  const [mapping, setMapping] = useState("");
  const [formError, setFormError] = useState<string | null>(null);
  const [confirmId, setConfirmId] = useState<string | null>(null);

  const sources: Source[] = list.data?.sources ?? [];
  const statusById = list.data?.statusById;

  // Toggle a row's enabled flag by re-upserting the whole row (Upsert replaces
  // the row on the wire; the server reconciles the supervisor synchronously).
  const toggleEnabled = (s: Source, enabled: boolean) => {
    upsert.mutate({
      id: s.id,
      type: s.type,
      name: s.name,
      url: s.url,
      mappingJson: s.mappingJson,
      enabled,
    });
  };

  const resetForm = () => {
    setAdding(false);
    setName("");
    setUrl(DEFAULT_URL);
    setProfile("lerobot");
    setMapping("");
    setFormError(null);
  };

  const submitAdd = async (e: React.FormEvent) => {
    e.preventDefault();
    setFormError(null);
    const mappingJson = profile === "lerobot" ? LEROBOT_MAPPING : mapping.trim();
    if (profile === "custom" && mappingJson !== "") {
      try {
        JSON.parse(mappingJson); // fail fast on obviously bad JSON before the RPC
      } catch {
        setFormError("mapping must be valid JSON");
        return;
      }
    }
    try {
      await upsert.mutateAsync({
        id: "",
        type: "foxglove",
        name: name.trim(),
        url: url.trim(),
        mappingJson,
        enabled: true, // a newly-added source connects immediately
      });
      resetForm();
    } catch (err) {
      setFormError(err instanceof Error ? err.message : "create failed");
    }
  };

  return (
    <section className="sources-card" data-testid="sources-card">
      <div className="sources-head">
        <h2 className="hw-section">
          <Radio size={15} className="sources-head-icon" /> Telemetry sources
        </h2>
        {!adding && (
          <button className="tok-btn tok-btn--accent" onClick={() => setAdding(true)} data-testid="source-add">
            <Plus size={14} /> Add source
          </button>
        )}
      </div>
      <p className="settings-hint">
        Connections the bench maintains to pull telemetry in — a Foxglove WebSocket server today
        (lerobot <code>--display_mode=foxglove</code>). Enable a source to connect, decode, map, and
        ingest it in-process.
      </p>

      {list.isError && (
        <div className="page-error">
          {list.error instanceof Error ? list.error.message : "failed to load sources"}
        </div>
      )}

      {adding && (
        <form className="source-form" onSubmit={submitAdd} data-testid="source-form">
          <div className="source-form-row">
            <label className="source-field">
              <span className="source-field-label">Name</span>
              <input
                className="tok-input"
                placeholder="lab bench"
                value={name}
                autoFocus
                onChange={(e) => setName(e.target.value)}
                data-testid="source-name"
              />
            </label>
            <label className="source-field">
              <span className="source-field-label">URL</span>
              <input
                className="tok-input"
                placeholder={DEFAULT_URL}
                value={url}
                onChange={(e) => setUrl(e.target.value)}
                data-testid="source-url"
              />
            </label>
            <label className="source-field">
              <span className="source-field-label">Mapping</span>
              <select
                className="tok-input"
                value={profile}
                onChange={(e) => setProfile(e.target.value as "lerobot" | "custom")}
                data-testid="source-profile"
              >
                <option value="lerobot">lerobot profile</option>
                <option value="custom">custom JSON</option>
              </select>
            </label>
          </div>
          {profile === "custom" && (
            <textarea
              className="source-mapping"
              placeholder={`{"rules":[ ... ]}`}
              value={mapping}
              onChange={(e) => setMapping(e.target.value)}
              rows={4}
              data-testid="source-mapping"
            />
          )}
          {formError && <div className="page-error">{formError}</div>}
          <div className="source-form-actions">
            <button
              className="tok-btn tok-btn--accent"
              type="submit"
              disabled={url.trim().length === 0 || upsert.isPending}
              data-testid="source-create"
            >
              {upsert.isPending ? "Adding…" : "Add source"}
            </button>
            <button className="tok-btn" type="button" onClick={resetForm}>
              Cancel
            </button>
          </div>
        </form>
      )}

      {sources.length === 0 && !list.isLoading && !adding && (
        <div className="page-empty">No telemetry sources yet.</div>
      )}

      {sources.length > 0 && (
        <table className="sources-table">
          <thead>
            <tr>
              <th>Status</th>
              <th>Name</th>
              <th>URL</th>
              <th className="sources-col-num">Frames</th>
              <th className="sources-col-num">Enabled</th>
              <th />
            </tr>
          </thead>
          <tbody>
            {sources.map((s) => {
              const st = statusById?.get(s.id);
              return (
                <tr key={s.id} data-testid={`source-row-${s.id}`}>
                  <td>
                    <StatusDot status={st} />
                  </td>
                  <td className="source-name">{s.name || <span className="source-unnamed">(unnamed)</span>}</td>
                  <td className="source-url">{s.url}</td>
                  <td className="sources-col-num" data-testid={`source-frames-${s.id}`}>
                    {st ? Number(st.framesIngested).toLocaleString() : "—"}
                  </td>
                  <td className="sources-col-num">
                    <input
                      type="checkbox"
                      checked={s.enabled}
                      onChange={(e) => toggleEnabled(s, e.target.checked)}
                      data-testid={`source-enabled-${s.id}`}
                      aria-label={`enable ${s.name || s.url}`}
                    />
                  </td>
                  <td className="source-row-actions">
                    {confirmId === s.id ? (
                      <>
                        <button
                          className="tok-btn tok-btn--danger"
                          disabled={del.isPending}
                          onClick={async () => {
                            await del.mutateAsync(s.id);
                            setConfirmId(null);
                          }}
                          data-testid={`source-confirm-${s.id}`}
                        >
                          Confirm
                        </button>
                        <button className="tok-btn" onClick={() => setConfirmId(null)}>
                          <X size={13} />
                        </button>
                      </>
                    ) : (
                      <button
                        className="tok-icon-btn tok-icon-btn--danger"
                        title="delete source"
                        onClick={() => setConfirmId(s.id)}
                        data-testid={`source-delete-${s.id}`}
                      >
                        <Trash2 size={14} />
                      </button>
                    )}
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      )}
    </section>
  );
}
