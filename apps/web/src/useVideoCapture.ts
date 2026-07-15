/**
 * useVideoCapture — camera capture + upload wiring.
 *
 * Owns the getUserMedia preview stream, the stop/start chunk recorder (behind
 * the injectable {@link CaptureAdapter}), and the bounded {@link UploadQueue}
 * that POSTs each finished chunk to /video/chunks fire-and-forget. State surfaced
 * to the panel: available cameras, the live preview stream, recording flag,
 * upload stats, and any error. The adapter is a prop so the browser-only bits
 * (MediaRecorder) are swapped for a fake in a real-browser harness; the queue
 * and sync math are unit tested independently.
 */

import { useCallback, useEffect, useRef, useState } from "react";
import {
  browserCaptureAdapter,
  type CameraDevice,
  type CaptureAdapter,
  type CaptureHandle,
} from "./captureAdapter";
import { UploadQueue, type UploadStats } from "./uploadQueue";
import { uploadChunk } from "./videoApi";

/** How long each self-contained chunk records (ms). */
const CHUNK_MS = 2000;
/** Buffered chunks before the queue drops the oldest. */
const MAX_QUEUE = 30;

export interface UseVideoCaptureArgs {
  baseUrl: string;
  /** Destination camera id (the upload key), e.g. "bench-cam". */
  cameraId: string;
  /** Injectable for tests; defaults to the real MediaRecorder-backed adapter. */
  adapter?: CaptureAdapter;
}

export interface VideoCaptureState {
  supported: boolean;
  cameras: CameraDevice[];
  deviceId: string | undefined;
  setDeviceId: (id: string | undefined) => void;
  recording: boolean;
  start: () => void;
  stop: () => void;
  stats: UploadStats;
  /** Live preview MediaStream (attach to a muted <video>), or null. */
  previewStream: MediaStream | null;
  error: string | null;
}

const ZERO_STATS: UploadStats = { sent: 0, failed: 0, dropped: 0, queued: 0 };

export function useVideoCapture(args: UseVideoCaptureArgs): VideoCaptureState {
  const { baseUrl, cameraId } = args;
  const adapter = args.adapter ?? browserCaptureAdapter;

  const [supported] = useState(() => adapter.isSupported());
  const [cameras, setCameras] = useState<CameraDevice[]>([]);
  const [deviceId, setDeviceId] = useState<string | undefined>(undefined);
  const [recording, setRecording] = useState(false);
  const [stats, setStats] = useState<UploadStats>(ZERO_STATS);
  const [previewStream, setPreviewStream] = useState<MediaStream | null>(null);
  const [error, setError] = useState<string | null>(null);

  const captureRef = useRef<CaptureHandle | null>(null);
  const streamRef = useRef<MediaStream | null>(null);
  const queueRef = useRef<UploadQueue | null>(null);
  // Keep the latest cameraId/baseUrl for the queue's upload closure.
  const cameraIdRef = useRef(cameraId);
  cameraIdRef.current = cameraId;
  const baseUrlRef = useRef(baseUrl);
  baseUrlRef.current = baseUrl;

  if (queueRef.current === null) {
    queueRef.current = new UploadQueue({
      upload: (item) =>
        uploadChunk(baseUrlRef.current, {
          cameraId: cameraIdRef.current,
          startNs: item.startNs,
          durationMs: item.durationMs,
          blob: item.blob,
        }).then(() => undefined),
      maxQueue: MAX_QUEUE,
      onStats: setStats,
    });
  }

  // Enumerate cameras once when supported (labels populate after a getUserMedia
  // grant, so this may return unlabeled ids until recording has been started).
  useEffect(() => {
    if (!supported) return;
    let alive = true;
    adapter
      .enumerateCameras()
      .then((list) => {
        if (alive) setCameras(list);
      })
      .catch(() => {
        /* enumeration is best-effort; the default camera still works */
      });
    return () => {
      alive = false;
    };
  }, [supported, adapter]);

  const stop = useCallback(() => {
    captureRef.current?.stop();
    captureRef.current = null;
    const s = streamRef.current;
    if (s) {
      for (const t of s.getTracks()) t.stop();
      streamRef.current = null;
    }
    setPreviewStream(null);
    setRecording(false);
  }, []);

  const start = useCallback(() => {
    if (!supported || recording) return;
    setError(null);
    void (async () => {
      try {
        const stream = await adapter.openStream(deviceId);
        streamRef.current = stream;
        setPreviewStream(stream);
        captureRef.current = adapter.startChunkedCapture({
          stream,
          chunkMs: CHUNK_MS,
          onChunk: (chunk) =>
            queueRef.current?.enqueue({
              cameraId: cameraIdRef.current,
              startNs: chunk.startNs,
              durationMs: chunk.durationMs,
              blob: chunk.blob,
            }),
          onError: (e) => setError(e.message),
        });
        setRecording(true);
      } catch (e) {
        setError(e instanceof Error ? e.message : String(e));
        setRecording(false);
      }
    })();
  }, [supported, recording, adapter, deviceId]);

  // Tear down on unmount.
  useEffect(() => stop, [stop]);

  return {
    supported,
    cameras,
    deviceId,
    setDeviceId,
    recording,
    start,
    stop,
    stats,
    previewStream,
    error,
  };
}
