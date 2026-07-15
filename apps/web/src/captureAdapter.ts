/**
 * Capture adapter — the ONLY module that touches getUserMedia / MediaRecorder.
 *
 * These APIs do not exist in jsdom, so the whole feature is isolated behind this
 * thin, injectable surface: the capture hook (useVideoCapture.ts) depends on the
 * {@link CaptureAdapter} interface, not on MediaRecorder directly, so it can be
 * mocked in tests. This module itself is exercised only in the real-browser pass
 * (see the checklist in the panel) — everything downstream of it (upload queue,
 * sync math) is unit tested with the adapter mocked out.
 *
 * SELF-CONTAINED CHUNK PATTERN (the important bit):
 *   A MediaRecorder started with a `timeslice` emits the first blob with the
 *   WebM/EBML header but every subsequent blob WITHOUT one — so only the first
 *   is independently playable. To get a stream of independently-playable ~2s
 *   chunks we instead run a stop/start CYCLE: start a fresh recorder, let it run
 *   ~chunkMs, stop it (its single `dataavailable`+`onstop` yields one complete,
 *   header-bearing WebM), hand that blob up stamped with the wall-clock epoch ns
 *   captured at record start, then immediately start the next recorder. Each
 *   chunk is thus a valid standalone file the <video> element can play and seek.
 */

/** A camera as reported by enumerateDevices. */
export interface CameraDevice {
  deviceId: string;
  label: string;
}

/** One finished, independently-playable chunk. */
export interface CapturedChunk {
  blob: Blob;
  /** Wall-clock epoch ns at the moment recording of this chunk began. */
  startNs: number;
  /** Measured wall-clock duration of the chunk. */
  durationMs: number;
}

export interface StartCaptureOptions {
  /** Live MediaStream to record + preview (owned by the caller). */
  stream: MediaStream;
  /** Target chunk length in ms (~2000). */
  chunkMs: number;
  onChunk: (chunk: CapturedChunk) => void;
  onError: (err: Error) => void;
}

/** Handle to a running capture; `stop()` finalizes the in-flight chunk. */
export interface CaptureHandle {
  stop: () => void;
}

/** The seam the capture hook depends on (mock this in tests). */
export interface CaptureAdapter {
  isSupported(): boolean;
  enumerateCameras(): Promise<CameraDevice[]>;
  openStream(deviceId?: string): Promise<MediaStream>;
  startChunkedCapture(opts: StartCaptureOptions): CaptureHandle;
}

/** Wall-clock epoch ns (ms precision; see the videoSync ns note). */
function nowNs(): number {
  return Date.now() * 1e6;
}

/** Pick a supported WebM mime, preferring VP9, then VP8, then bare webm. */
function pickMimeType(): string {
  const rec = (globalThis as { MediaRecorder?: typeof MediaRecorder }).MediaRecorder;
  const candidates = ["video/webm;codecs=vp9", "video/webm;codecs=vp8", "video/webm"];
  if (rec && typeof rec.isTypeSupported === "function") {
    for (const c of candidates) if (rec.isTypeSupported(c)) return c;
  }
  return "video/webm";
}

/** The real, browser-backed adapter used by the app. */
export const browserCaptureAdapter: CaptureAdapter = {
  isSupported(): boolean {
    return (
      typeof navigator !== "undefined" &&
      !!navigator.mediaDevices &&
      typeof navigator.mediaDevices.getUserMedia === "function" &&
      typeof (globalThis as { MediaRecorder?: unknown }).MediaRecorder !== "undefined"
    );
  },

  async enumerateCameras(): Promise<CameraDevice[]> {
    const devices = await navigator.mediaDevices.enumerateDevices();
    return devices
      .filter((d) => d.kind === "videoinput")
      .map((d, i) => ({ deviceId: d.deviceId, label: d.label || `camera ${i + 1}` }));
  },

  async openStream(deviceId?: string): Promise<MediaStream> {
    const video: MediaTrackConstraints = deviceId
      ? { deviceId: { exact: deviceId } }
      : { facingMode: "environment" };
    return navigator.mediaDevices.getUserMedia({ video, audio: false });
  },

  startChunkedCapture(opts: StartCaptureOptions): CaptureHandle {
    const { stream, chunkMs, onChunk, onError } = opts;
    const mimeType = pickMimeType();
    let stopped = false;
    let timer: ReturnType<typeof setTimeout> | null = null;
    let recorder: MediaRecorder | null = null;

    const cycle = (): void => {
      if (stopped) return;
      let rec: MediaRecorder;
      try {
        rec = new MediaRecorder(stream, { mimeType });
      } catch (err) {
        onError(err instanceof Error ? err : new Error(String(err)));
        return;
      }
      recorder = rec;
      const parts: Blob[] = [];
      const startNs = nowNs();

      rec.ondataavailable = (e: BlobEvent) => {
        if (e.data && e.data.size > 0) parts.push(e.data);
      };
      rec.onerror = () => onError(new Error("MediaRecorder error"));
      rec.onstop = () => {
        const blob = new Blob(parts, { type: mimeType });
        const durationMs = (nowNs() - startNs) / 1e6;
        if (blob.size > 0) onChunk({ blob, startNs, durationMs });
        // Chain the next chunk immediately so coverage is continuous.
        cycle();
      };

      rec.start();
      timer = setTimeout(() => {
        if (rec.state !== "inactive") rec.stop();
      }, chunkMs);
    };

    cycle();

    return {
      stop(): void {
        stopped = true;
        if (timer) clearTimeout(timer);
        if (recorder && recorder.state !== "inactive") recorder.stop();
      },
    };
  },
};
