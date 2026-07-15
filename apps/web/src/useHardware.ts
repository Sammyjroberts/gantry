import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  Code,
  ConnectError,
  createHardwareClient,
  type Hardware,
  type HardwareClient,
} from "@gantry/api-client";
import type { PoseBindings } from "./pose";
import {
  clearLegacyBindings,
  decodePanelDefaults,
  decodeVizConfig,
  defaultBindings,
  deviceDisplayName,
  encodePanelDefaults,
  encodeVizConfig,
  readLegacyBindings,
} from "./hardware";

export interface UseHardwareArgs {
  baseUrl: string;
  /** Background refresh cadence in ms (default 10s). Set 0 to disable polling. */
  pollMs?: number;
}

/** A partial hardware edit; only device_id is required. Omitted opaque JSON
 *  fields are preserved from the known row (never clobbered — Upsert replaces
 *  the whole row on the wire, so the hook must send a merged message). */
export interface HardwarePatch {
  deviceId: string;
  displayName?: string;
  description?: string;
  notes?: string;
  vizConfigJson?: string;
  panelDefaultsJson?: string;
}

export interface UseHardwareResult {
  hardware: Hardware[];
  byId: Map<string, Hardware>;
  /** Devices seen in telemetry with no hardware row yet (promotable). */
  unconfigured: string[];
  loading: boolean;
  error: string | null;
  refresh: () => Promise<void>;
  /** Create-or-update, merging the patch onto the known row. */
  upsert: (patch: HardwarePatch) => Promise<Hardware | null>;
  remove: (deviceId: string) => Promise<void>;
  /** Human label for a device: display_name if set, else the device id. */
  displayName: (deviceId: string) => string;
  // ---- 3D viz config (server-homed pose bindings) ----
  /** Load a device's bindings from the server, migrating legacy localStorage once. */
  loadVizConfig: (device: string) => Promise<PoseBindings>;
  /** Persist a device's bindings into viz_config_json (merged upsert). */
  saveVizConfig: (device: string, bindings: PoseBindings) => Promise<void>;
  // ---- panel defaults (per-device channel selection) ----
  /** The saved default channel-key selection for a device (empty when none). */
  panelDefaults: (device: string) => string[];
  /** Save a channel-key selection as the device's panel default (merged upsert). */
  savePanelDefaults: (device: string, channels: string[]) => Promise<void>;
}

function isNotFound(err: unknown): boolean {
  return err instanceof ConnectError && err.code === Code.NotFound;
}

/**
 * Hardware store bound to HardwareService. Owns the configured-device list, the
 * unconfigured (seen-but-unconfigured) set, and the mutations behind the
 * hardware page — plus the two server-homed config documents the console layers
 * on top: the 3D viz bindings (viz_config_json) and per-device panel defaults
 * (panel_defaults_json).
 *
 * Upsert replaces a whole row on the wire, so every mutation merges its patch
 * onto the last-known row before sending, and merges the server's authoritative
 * result back into local state. Mirrors useExperiments' transport shape: one
 * client per baseUrl, errors surfaced as strings, mutations awaitable.
 */
export function useHardware(args: UseHardwareArgs): UseHardwareResult {
  const { baseUrl, pollMs = 10_000 } = args;

  const [hardware, setHardware] = useState<Hardware[]>([]);
  const [unconfigured, setUnconfigured] = useState<string[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const clientRef = useRef<HardwareClient | null>(null);
  if (clientRef.current === null) clientRef.current = createHardwareClient(baseUrl);
  const baseUrlRef = useRef(baseUrl);
  if (baseUrlRef.current !== baseUrl) {
    baseUrlRef.current = baseUrl;
    clientRef.current = createHardwareClient(baseUrl);
  }

  const byId = useMemo(() => {
    const m = new Map<string, Hardware>();
    for (const hw of hardware) m.set(hw.deviceId, hw);
    return m;
  }, [hardware]);

  // Mirror byId into a ref so the stable callbacks below read fresh rows without
  // being re-created (and re-triggering effects) on every list change.
  const byIdRef = useRef(byId);
  byIdRef.current = byId;

  const mergeRow = useCallback((hw: Hardware) => {
    setHardware((prev) => {
      const next = prev.filter((h) => h.deviceId !== hw.deviceId);
      next.push(hw);
      next.sort((a, b) => {
        const an = (a.displayName || a.deviceId).toLowerCase();
        const bn = (b.displayName || b.deviceId).toLowerCase();
        return an < bn ? -1 : an > bn ? 1 : a.deviceId < b.deviceId ? -1 : 1;
      });
      return next;
    });
    // A newly-configured device leaves the unconfigured set immediately.
    setUnconfigured((prev) => prev.filter((id) => id !== hw.deviceId));
  }, []);

  const refresh = useCallback(async (): Promise<void> => {
    const client = clientRef.current!;
    try {
      const res = await client.listHardware({});
      setHardware(res.hardware);
      setUnconfigured(res.unconfiguredDeviceIds);
      setError(null);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    let stopped = false;
    void refresh();
    if (pollMs <= 0) return;
    const id = setInterval(() => {
      if (!stopped) void refresh();
    }, pollMs);
    return () => {
      stopped = true;
      clearInterval(id);
    };
  }, [refresh, pollMs]);

  // Core merged upsert: send a full row (patch over the last-known row) and fold
  // the server's authoritative result back in. Stable (reads rows via the ref).
  const upsert = useCallback(async (patch: HardwarePatch): Promise<Hardware | null> => {
    const client = clientRef.current!;
    const cur = byIdRef.current.get(patch.deviceId);
    const msg = {
      deviceId: patch.deviceId,
      displayName: patch.displayName ?? cur?.displayName ?? "",
      description: patch.description ?? cur?.description ?? "",
      notes: patch.notes ?? cur?.notes ?? "",
      vizConfigJson: patch.vizConfigJson ?? cur?.vizConfigJson ?? "",
      panelDefaultsJson: patch.panelDefaultsJson ?? cur?.panelDefaultsJson ?? "",
    };
    try {
      const res = await client.upsertHardware({ hardware: msg });
      setError(null);
      if (res.hardware) {
        mergeRow(res.hardware);
        return res.hardware;
      }
      return null;
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
      return null;
    }
  }, [mergeRow]);

  const remove = useCallback(async (deviceId: string): Promise<void> => {
    const client = clientRef.current!;
    try {
      await client.deleteHardware({ deviceId });
      setHardware((prev) => prev.filter((h) => h.deviceId !== deviceId));
      setError(null);
      // Re-list so the device reappears as unconfigured if still emitting.
      void refresh();
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    }
  }, [refresh]);

  const displayName = useCallback(
    (deviceId: string) => deviceDisplayName(deviceId, byIdRef.current),
    [],
  );

  // Persist bindings into viz_config_json via a merged upsert (stable).
  const saveVizConfig = useCallback(
    async (device: string, bindings: PoseBindings): Promise<void> => {
      await upsert({ deviceId: device, vizConfigJson: encodeVizConfig(bindings) });
    },
    [upsert],
  );

  // Load a device's bindings authoritatively from the server. On a definitive
  // empty (row absent → NotFound, or present-but-empty viz), run the ONE-TIME
  // localStorage→server migration: push the legacy blob up, then remove it. A
  // transient error touches nothing (returns defaults, keeps localStorage).
  const loadVizConfig = useCallback(
    async (device: string): Promise<PoseBindings> => {
      const client = clientRef.current!;
      let serverJson = "";
      let known = false;
      try {
        const res = await client.getHardware({ deviceId: device });
        serverJson = res.hardware?.vizConfigJson ?? "";
        known = true;
      } catch (err) {
        if (isNotFound(err)) {
          known = true; // definitively no row yet
        } else {
          setError(err instanceof Error ? err.message : String(err));
        }
      }
      const decoded = decodeVizConfig(serverJson);
      if (decoded) return decoded;
      if (!known) return defaultBindings(); // transient failure: do not migrate
      const legacy = readLegacyBindings(device);
      if (legacy) {
        try {
          await saveVizConfig(device, legacy);
          clearLegacyBindings(device);
        } catch {
          /* leave localStorage in place for a later retry */
        }
        return legacy;
      }
      return defaultBindings();
    },
    [saveVizConfig],
  );

  const panelDefaults = useCallback(
    (device: string) => decodePanelDefaults(byIdRef.current.get(device)?.panelDefaultsJson),
    [],
  );

  const savePanelDefaults = useCallback(
    async (device: string, channels: string[]): Promise<void> => {
      await upsert({ deviceId: device, panelDefaultsJson: encodePanelDefaults(channels) });
    },
    [upsert],
  );

  return {
    hardware,
    byId,
    unconfigured,
    loading,
    error,
    refresh,
    upsert,
    remove,
    displayName,
    loadVizConfig,
    saveVizConfig,
    panelDefaults,
    savePanelDefaults,
  };
}
