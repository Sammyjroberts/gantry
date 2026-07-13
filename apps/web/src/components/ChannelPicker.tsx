import { useState } from "react";
import type { DeviceChannels } from "@gantry/api-client";
import { kindLabel, isPlottable } from "../valueKind";
import {
  groupByPacket,
  channelKey,
  type ChannelId,
} from "../channel";

export interface ChannelPickerProps {
  devices: DeviceChannels[];
  /** Selected channels, keyed by (packet, name) — see channel.ts. */
  selected: Set<string>;
  onToggle: (id: ChannelId) => void;
  error: string | null;
}

/**
 * Sidebar channel tree: device → packet (collapsible) → params. Ad-hoc channels
 * (empty packet) live under an "ad hoc" bucket. Identity is (packet, name), so
 * imu.temp and power.temp are distinct rows with independent selection.
 */
export function ChannelPicker({ devices, selected, onToggle, error }: ChannelPickerProps) {
  // Collapsed packet groups, keyed by `${deviceId}${packet}`.
  const [collapsed, setCollapsed] = useState<Set<string>>(new Set());
  const toggleCollapse = (key: string) =>
    setCollapsed((prev) => {
      const next = new Set(prev);
      if (next.has(key)) next.delete(key);
      else next.add(key);
      return next;
    });

  return (
    <aside className="picker">
      <div className="picker-head">CHANNELS</div>
      {error && <div className="picker-error">list failed: {error}</div>}
      {!error && devices.length === 0 && (
        <div className="picker-empty">no devices reported</div>
      )}
      {devices.map((dev) => (
        <div className="device-group" key={dev.deviceId}>
          <div className="device-name">{dev.deviceId || "(unnamed device)"}</div>
          {groupByPacket(dev.channels).map((group) => {
            const groupKey = `${dev.deviceId}${group.packet}`;
            const isCollapsed = collapsed.has(groupKey);
            const label = group.adHoc ? "ad hoc" : group.packet;
            return (
              <div className="packet-group" key={groupKey}>
                <button
                  type="button"
                  className={`packet-head ${group.adHoc ? "packet-adhoc" : ""}`}
                  aria-expanded={!isCollapsed}
                  onClick={() => toggleCollapse(groupKey)}
                >
                  <span className="packet-caret">{isCollapsed ? "▸" : "▾"}</span>
                  <span className="packet-name">{label}</span>
                  <span className="packet-count">{group.channels.length}</span>
                </button>
                {!isCollapsed && (
                  <ul className="channel-list">
                    {group.channels.map((ch) => {
                      const key = channelKey(ch.packet, ch.name);
                      const on = selected.has(key);
                      const plottable = isPlottable(ch.kind);
                      return (
                        <li key={key} className={plottable ? "" : "channel-nonplot"}>
                          <label title={ch.description || ch.name}>
                            <input
                              type="checkbox"
                              checked={on}
                              onChange={() =>
                                onToggle({ packet: ch.packet, name: ch.name })
                              }
                            />
                            <span className="channel-name">{ch.name}</span>
                            <span className="channel-meta">
                              <span className="channel-kind">{kindLabel(ch.kind)}</span>
                              {ch.unit && <span className="channel-unit">{ch.unit}</span>}
                            </span>
                          </label>
                        </li>
                      );
                    })}
                  </ul>
                )}
              </div>
            );
          })}
        </div>
      ))}
    </aside>
  );
}
