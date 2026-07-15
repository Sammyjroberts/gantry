import { useCallback, useMemo, useRef } from "react";
import {
  Code,
  ConnectError,
  createHardwareClient,
  type HardwareClient,
} from "@gantry/api-client";
import { resolveBaseUrl } from "../config";
import type { PoseBindings } from "../pose";
import {
  clearLegacyBindings,
  decodeVizConfig,
  defaultBindings,
  encodeVizConfig,
  readLegacyBindings,
} from "../hardware";

/**
 * Standalone load/save for a device's 3D pose bindings (HardwareService
 * viz_config_json), extracted so the 3D panel and the hardware detail page can
 * drive Scene3D without pulling in the whole hardware-list polling hook. Mirrors
 * useHardware's viz methods: merged Upsert, one-time localStorage→server
 * migration, tolerant decode.
 */
export function useVizConfig() {
  const baseUrl = useMemo(resolveBaseUrl, []);
  const clientRef = useRef<HardwareClient | null>(null);
  if (clientRef.current === null) clientRef.current = createHardwareClient(baseUrl);

  const saveVizConfig = useCallback(
    async (device: string, bindings: PoseBindings): Promise<void> => {
      const client = clientRef.current!;
      // Merge onto the known row so we never clobber name/description/etc.
      let cur;
      try {
        const res = await client.getHardware({ deviceId: device });
        cur = res.hardware;
      } catch {
        cur = undefined;
      }
      await client.upsertHardware({
        hardware: {
          deviceId: device,
          displayName: cur?.displayName ?? "",
          description: cur?.description ?? "",
          notes: cur?.notes ?? "",
          vizConfigJson: encodeVizConfig(bindings),
          panelDefaultsJson: cur?.panelDefaultsJson ?? "",
        },
      });
    },
    [],
  );

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
        if (err instanceof ConnectError && err.code === Code.NotFound) known = true;
      }
      const decoded = decodeVizConfig(serverJson);
      if (decoded) return decoded;
      if (!known) return defaultBindings();
      const legacy = readLegacyBindings(device);
      if (legacy) {
        try {
          await saveVizConfig(device, legacy);
          clearLegacyBindings(device);
        } catch {
          /* leave localStorage for a later retry */
        }
        return legacy;
      }
      return defaultBindings();
    },
    [saveVizConfig],
  );

  return { loadVizConfig, saveVizConfig };
}
