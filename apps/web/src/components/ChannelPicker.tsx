import type { DeviceChannels } from "@gantry/api-client";
import { kindLabel, isPlottable } from "../valueKind";

export interface ChannelPickerProps {
  devices: DeviceChannels[];
  selected: Set<string>;
  onToggle: (channel: string) => void;
  error: string | null;
}

/** Sidebar: devices as groups, one checkbox per channel with kind + unit. */
export function ChannelPicker({ devices, selected, onToggle, error }: ChannelPickerProps) {
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
          <ul className="channel-list">
            {dev.channels.map((ch) => {
              const on = selected.has(ch.name);
              const plottable = isPlottable(ch.kind);
              return (
                <li key={ch.name} className={plottable ? "" : "channel-nonplot"}>
                  <label title={ch.description || ch.name}>
                    <input
                      type="checkbox"
                      checked={on}
                      onChange={() => onToggle(ch.name)}
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
        </div>
      ))}
    </aside>
  );
}
