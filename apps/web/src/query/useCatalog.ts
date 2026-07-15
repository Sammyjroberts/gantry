import { useMemo } from "react";
import { useQuery } from "@tanstack/react-query";
import {
  createLiveClient,
  ValueKind,
  type DeviceChannels,
} from "@gantry/api-client";
import { resolveBaseUrl } from "../config";
import { channelLabel, infoKey } from "../channel";
import { buildCatalogIndex, type CatalogIndex } from "../workspace/layout";
import type { ChannelOption } from "../scene3dControls";

/** Per-channel metadata projected from the catalogue, keyed by (packet,name). */
export interface ChannelMeta {
  unit: string;
  kind: ValueKind;
  device: string;
  packet: string;
  name: string;
}

export interface CatalogResult {
  devices: DeviceChannels[];
  deviceIds: string[];
  multiDevice: boolean;
  /** Resolvable channel keys, for panel binding resolution. */
  catalogIndex: CatalogIndex;
  /** Flat channel options for pickers (device-prefixed when multi-device). */
  channelOptions: ChannelOption[];
  metaByChannel: Map<string, ChannelMeta>;
  isLoading: boolean;
  error: string | null;
}

/**
 * The channel catalogue as TanStack Query server state. Polls every 15s so
 * newly-seen devices/channels appear without a reload (catalogue liveness), and
 * derives the binding index / picker options / metadata the whole console reads.
 */
export function useCatalog(): CatalogResult {
  const baseUrl = useMemo(resolveBaseUrl, []);
  const query = useQuery({
    queryKey: ["catalog"],
    queryFn: async ({ signal }) => {
      const client = createLiveClient(baseUrl);
      const res = await client.listChannels({ deviceId: "" }, { signal });
      return res.devices;
    },
    refetchInterval: 15_000,
    staleTime: 10_000,
  });

  const devices = query.data ?? [];

  const derived = useMemo(() => {
    const deviceIds = [...new Set(devices.map((d) => d.deviceId).filter(Boolean))];
    const multiDevice = devices.length > 1;
    const metaByChannel = new Map<string, ChannelMeta>();
    const options: ChannelOption[] = [];
    const flat: { deviceId: string; packet: string; channel: string }[] = [];
    for (const d of devices) {
      for (const c of d.channels) {
        const key = infoKey(c);
        metaByChannel.set(key, {
          unit: c.unit,
          kind: c.kind,
          device: d.deviceId,
          packet: c.packet,
          name: c.name,
        });
        const base = channelLabel(c.packet, c.name);
        options.push({
          key,
          label: multiDevice && d.deviceId ? `${d.deviceId} · ${base}` : base,
          device: d.deviceId,
          unit: c.unit,
        });
        flat.push({ deviceId: d.deviceId, packet: c.packet, channel: c.name });
      }
    }
    return {
      deviceIds,
      multiDevice,
      metaByChannel,
      channelOptions: options,
      catalogIndex: buildCatalogIndex(flat) as CatalogIndex,
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [devices]);

  return {
    devices,
    ...derived,
    isLoading: query.isLoading,
    error: query.error ? (query.error as Error).message : null,
  };
}
