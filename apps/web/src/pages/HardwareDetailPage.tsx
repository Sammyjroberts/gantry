import { Suspense, lazy, useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Link, useParams } from "react-router-dom";
import { ArrowLeft, Save } from "lucide-react";
import { resolveBaseUrl } from "../config";
import { useHardware } from "../useHardware";
import { useCatalog } from "../query/useCatalog";
import { useLive } from "../live/LiveContext";
import { useVizConfig } from "../hooks/useVizConfig";
import { useExtraKeysStore } from "../store/extraKeys";
import { listModels } from "../models";
import { resolveModelSource } from "../pose";
import type { Sampler } from "../pose";

const Scene3D = lazy(() => import("../Scene3D"));

/**
 * The hardware detail / configuration page — the home for setting up a machine:
 * its identity (name / description / notes) and its 3D visualization (model
 * source, URDF editor, pose/joint bindings, live preview). The 3D config reuses
 * the Scene3D module scoped to this device, so the URDF editor, binding editor
 * and live preview relocate here intact. Bindings persist to viz_config_json;
 * identity persists via HardwareService.
 */
export function HardwareDetailPage() {
  const { deviceId = "" } = useParams();
  const baseUrl = useMemo(resolveBaseUrl, []);
  const hw = useHardware({ baseUrl });
  const { channelOptions } = useCatalog();
  const { store } = useLive();
  const { loadVizConfig, saveVizConfig } = useVizConfig();
  const setExtra = useExtraKeysStore((s) => s.set);
  const clearExtra = useExtraKeysStore((s) => s.clear);

  const row = hw.byId.get(deviceId);
  const [displayName, setDisplayName] = useState("");
  const [description, setDescription] = useState("");
  const [notes, setNotes] = useState("");
  const [seeded, setSeeded] = useState(false);
  const [saving, setSaving] = useState(false);
  const [models, setModels] = useState<string[]>([]);

  // Seed the form once the row loads (or immediately for an unconfigured device).
  useEffect(() => {
    if (seeded) return;
    if (row) {
      setDisplayName(row.displayName);
      setDescription(row.description);
      setNotes(row.notes);
      setSeeded(true);
    } else if (!hw.loading) {
      setSeeded(true);
    }
  }, [row, hw.loading, seeded]);

  useEffect(() => {
    const ac = new AbortController();
    listModels(baseUrl, ac.signal).then(setModels).catch(() => setModels([]));
    return () => ac.abort();
  }, [baseUrl]);

  const modelSource = useMemo(() => resolveModelSource(deviceId, models), [deviceId, models]);

  // Live sampler for the 3D preview (latest ring value per bound channel).
  const sampleRef = useRef<Sampler>(() => null);
  sampleRef.current = (key: string) => store.get(key)?.latest()?.value ?? null;

  const extraId = `hw-detail-${deviceId}`;
  useEffect(() => () => clearExtra(extraId), [extraId, clearExtra]);

  // MUST be stable: this page consumes useLive(), which re-renders when
  // setExtra mutates the subscription; an unmemoized closure would give Scene3D
  // a fresh identity each render and loop its binding effect (React #185).
  const onBoundChannelsChange = useCallback(
    (keys: string[]) => setExtra(extraId, keys),
    [setExtra, extraId],
  );

  const save = async () => {
    setSaving(true);
    await hw.upsert({ deviceId, displayName: displayName.trim(), description, notes });
    setSaving(false);
  };

  return (
    <div className="page hardware-detail-page">
      <div className="hw-detail-head">
        <Link to="/hardware" className="hw-back">
          <ArrowLeft size={16} /> Hardware
        </Link>
        <span className="hw-detail-id">{deviceId}</span>
      </div>

      <div className="hw-detail-grid">
        <section className="hw-detail-form">
          <h2 className="hw-section">Identity</h2>
          <label className="hw-field">
            <span>Display name</span>
            <input value={displayName} onChange={(e) => setDisplayName(e.target.value)} placeholder={deviceId} />
          </label>
          <label className="hw-field">
            <span>Description</span>
            <input value={description} onChange={(e) => setDescription(e.target.value)} />
          </label>
          <label className="hw-field">
            <span>Notes</span>
            <textarea value={notes} onChange={(e) => setNotes(e.target.value)} rows={4} />
          </label>
          <button className="hw-save" onClick={() => void save()} disabled={saving} data-testid="hw-save">
            <Save size={14} /> {saving ? "saving…" : "save"}
          </button>

          <h2 className="hw-section">Model</h2>
          <div className="hw-model-status">
            <div>
              source: <code>{modelSource.kind}</code>
              {modelSource.file && <span className="hw-model-file"> · {modelSource.file}</span>}
            </div>
            <div className="hw-model-hint">
              {modelSource.kind === "primitive"
                ? "No model file for this device — using the generated primitive. Add a <device>.urdf/.glb/.stl in the model directory (edit URDF below)."
                : "Resolved from the model directory."}
            </div>
          </div>
        </section>

        <section className="hw-detail-3d">
          <h2 className="hw-section">3D configuration &amp; preview</h2>
          <div className="hw-scene-host">
            <Suspense fallback={<div className="scene3d-loading">loading 3D module…</div>}>
              <Scene3D
                baseUrl={baseUrl}
                devices={[deviceId]}
                channels={channelOptions}
                sampleRef={sampleRef}
                replaying={false}
                onBoundChannelsChange={onBoundChannelsChange}
                loadVizConfig={loadVizConfig}
                saveVizConfig={saveVizConfig}
                onClose={() => {}}
              />
            </Suspense>
          </div>
        </section>
      </div>
    </div>
  );
}
