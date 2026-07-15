/**
 * VideoPanel — camera capture + replay/live-follow viewer (lazy dock, like
 * Scene3D). Default-exported so App can pull it in via React.lazy, keeping it out
 * of the main bundle until the VIDEO toggle mounts it.
 *
 * Three cooperating pieces, each behind its own hook so this file is thin glue:
 *  - CAPTURE (useVideoCapture): getUserMedia preview + stop/start ~2s chunk
 *    recorder → bounded upload queue → POST /video/chunks. Record dot + stats.
 *  - REPLAY (useVideoReplay): when App is replaying, the viewer <video> is
 *    slaved to the playback cursor — chunk lookup, seek, playbackRate, gaps.
 *  - LIVE-FOLLOW (useVideoFollow): otherwise, watch a chosen camera near its
 *    live edge, newest-chunk-first, with a lag badge.
 * Replay takes precedence over follow (you can't scrub the past and chase the
 * live edge at once), so follow is disabled while a replay is active.
 */

import { useEffect, useMemo, useRef, useState } from "react";
import { listServerCameras, type ServerCamera } from "./videoApi";
import { useVideoCapture } from "./useVideoCapture";
import { useVideoFollow } from "./useVideoFollow";
import { useVideoReplay, type ReplayView } from "./useVideoReplay";

export interface VideoPanelProps {
  baseUrl: string;
  /** Active replay session (drives cursor-synced playback), or null. */
  replay: ReplayView | null;
  /** Seed the capture/watch camera (from a video panel's config). */
  initialCamera?: string;
  onClose: () => void;
}

const DEFAULT_CAMERA = "bench-cam";

export default function VideoPanel({ baseUrl, replay, initialCamera, onClose }: VideoPanelProps) {
  const seedCamera = initialCamera && initialCamera.trim() ? initialCamera.trim() : DEFAULT_CAMERA;
  const [cameraId, setCameraId] = useState(seedCamera);
  const [watchCamera, setWatchCamera] = useState(seedCamera);
  const [following, setFollowing] = useState(false);
  const [serverCameras, setServerCameras] = useState<ServerCamera[]>([]);

  const previewRef = useRef<HTMLVideoElement | null>(null);
  const viewerRef = useRef<HTMLVideoElement | null>(null);

  const capture = useVideoCapture({ baseUrl, cameraId });

  // Attach the live preview stream to the muted preview element.
  useEffect(() => {
    const v = previewRef.current;
    if (v) v.srcObject = capture.previewStream;
  }, [capture.previewStream]);

  // Server-side camera catalogue for the watch selector.
  useEffect(() => {
    const ac = new AbortController();
    listServerCameras(baseUrl, ac.signal)
      .then(setServerCameras)
      .catch(() => undefined);
    return () => ac.abort();
  }, [baseUrl]);

  const replaying = replay !== null;
  // Follow is suppressed during replay; the viewer is driven by the cursor.
  const follow = useVideoFollow({
    baseUrl,
    cameraId: watchCamera,
    videoRef: viewerRef,
    enabled: following && !replaying,
  });
  const replaySync = useVideoReplay({
    baseUrl,
    cameraId: watchCamera,
    videoRef: viewerRef,
    replay,
  });

  // The gap overlay shows when replay lands on a missing chunk, or when nothing
  // is playing yet in follow mode.
  const showGap = replaying ? !replaySync.loading && !replaySync.hasVideo : following && !follow.active;

  const cameraOptions = useMemo(() => {
    const ids = new Set(serverCameras.map((c) => c.cameraId));
    ids.add(watchCamera);
    return [...ids];
  }, [serverCameras, watchCamera]);

  const stats = capture.stats;

  return (
    <div className="video-panel">
      <div className="video-head">
        <span className="video-title">◉ VIDEO</span>
        {replaying && <span className="video-mode video-mode--replay">▶ replay-sync</span>}
        <button className="video-close" onClick={onClose} title="close video panel">
          ✕
        </button>
      </div>

      {/* ---- capture ---- */}
      <div className="video-section">
        <div className="video-section-head">
          <span>capture</span>
          {capture.recording && <span className="video-rec"><span className="video-rec-dot" /> REC</span>}
        </div>

        {!capture.supported && (
          <div className="video-note">
            camera capture needs a real browser (getUserMedia / MediaRecorder).
          </div>
        )}

        <div className="video-row">
          <label className="video-lbl">camera id</label>
          <input
            className="video-input"
            value={cameraId}
            onChange={(e) => setCameraId(e.target.value.trim() || DEFAULT_CAMERA)}
            disabled={capture.recording}
            title="upload key for captured chunks"
          />
        </div>

        {capture.cameras.length > 0 && (
          <div className="video-row">
            <label className="video-lbl">source</label>
            <select
              className="video-select"
              value={capture.deviceId ?? ""}
              onChange={(e) => capture.setDeviceId(e.target.value || undefined)}
              disabled={capture.recording}
            >
              <option value="">default</option>
              {capture.cameras.map((c) => (
                <option key={c.deviceId} value={c.deviceId}>
                  {c.label}
                </option>
              ))}
            </select>
          </div>
        )}

        <div className="video-preview-wrap">
          <video ref={previewRef} className="video-preview" muted autoPlay playsInline />
          {!capture.recording && <div className="video-preview-idle">preview</div>}
        </div>

        <div className="video-actions">
          {capture.recording ? (
            <button className="video-btn video-btn--stop" onClick={capture.stop}>
              ❚❚ stop
            </button>
          ) : (
            <button
              className="video-btn video-btn--rec"
              onClick={capture.start}
              disabled={!capture.supported}
            >
              ● record
            </button>
          )}
          <span className="video-stats" title="upload queue: sent / failed / dropped / buffered">
            <span className="video-stat"><b>{stats.sent}</b> sent</span>
            <span className={`video-stat ${stats.failed ? "is-bad" : ""}`}><b>{stats.failed}</b> failed</span>
            <span className={`video-stat ${stats.dropped ? "is-warn" : ""}`}><b>{stats.dropped}</b> dropped</span>
            <span className="video-stat"><b>{stats.queued}</b> queued</span>
          </span>
        </div>
        {capture.error && <div className="video-err">⚠ {capture.error}</div>}
      </div>

      {/* ---- watch (replay-synced or live-follow) ---- */}
      <div className="video-section">
        <div className="video-section-head">
          <span>watch</span>
          {replaying ? (
            <span className="video-badge video-badge--replay">cursor-synced</span>
          ) : (
            follow.active && (
              <span className="video-badge video-badge--live">
                LIVE-ish{follow.lagSec !== null ? ` +${follow.lagSec.toFixed(1)}s` : ""}
              </span>
            )
          )}
        </div>

        <div className="video-row">
          <label className="video-lbl">camera</label>
          <select
            className="video-select"
            value={watchCamera}
            onChange={(e) => setWatchCamera(e.target.value)}
          >
            {cameraOptions.map((id) => (
              <option key={id} value={id}>
                {id}
              </option>
            ))}
          </select>
          {!replaying && (
            <button
              className={`video-btn ${following ? "video-btn--on" : ""}`}
              onClick={() => setFollowing((f) => !f)}
            >
              {following ? "following" : "follow"}
            </button>
          )}
        </div>

        <div className="video-viewer-wrap">
          <video ref={viewerRef} className="video-viewer" playsInline controls={false} />
          {showGap && (
            <div className="video-gap">
              <span className="video-gap-mark">▦</span>
              no video
            </div>
          )}
          {replaying && replaySync.loading && (
            <div className="video-gap video-gap--loading">buffering chunks…</div>
          )}
        </div>
        {(follow.error || replaySync.error) && (
          <div className="video-err">⚠ {follow.error ?? replaySync.error}</div>
        )}
      </div>
    </div>
  );
}
