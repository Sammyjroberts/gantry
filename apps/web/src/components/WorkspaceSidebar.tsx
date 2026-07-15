import { useMemo, useState } from "react";
import { PanelLeftClose, PanelLeftOpen, LineChart } from "lucide-react";
import type { DeviceChannels } from "@gantry/api-client";
import { ChannelPicker } from "./ChannelPicker";
import { useCatalog } from "../query/useCatalog";
import { useWorkspaceStore } from "../store/workspaceStore";
import { channelKey, parseKey } from "../channel";
import type { PanelBinding } from "../workspace/layout";

/**
 * The workspace channel sidebar: the grouped catalogue (reusing ChannelPicker)
 * with a filter box and an "add as chart" action. Selection lives in the
 * workspace store and feeds both the add-as-chart flow and the live
 * subscription union. Collapsible.
 */
export function WorkspaceSidebar() {
  const { devices, metaByChannel, error } = useCatalog();
  const selection = useWorkspaceStore((s) => s.selection);
  const toggleSelection = useWorkspaceStore((s) => s.toggleSelection);
  const clearSelection = useWorkspaceStore((s) => s.clearSelection);
  const addPanel = useWorkspaceStore((s) => s.addPanel);
  const setEditing = useWorkspaceStore((s) => s.setEditing);
  const [collapsed, setCollapsed] = useState(false);
  const [filter, setFilter] = useState("");

  const filtered = useMemo<DeviceChannels[]>(() => {
    const q = filter.trim().toLowerCase();
    if (!q) return devices;
    return devices
      .map((d) => ({
        ...d,
        channels: d.channels.filter(
          (c) =>
            c.name.toLowerCase().includes(q) ||
            c.packet.toLowerCase().includes(q) ||
            `${c.packet}.${c.name}`.toLowerCase().includes(q),
        ),
      }))
      .filter((d) => d.channels.length > 0);
  }, [devices, filter]);

  const addAsChart = () => {
    const channels: PanelBinding[] = [];
    for (const key of selection) {
      const id = parseKey(key);
      const meta = metaByChannel.get(key);
      channels.push({ deviceId: meta?.device ?? "", packet: id.packet, channel: id.name });
    }
    if (channels.length === 0) return;
    addPanel("timeseries", { channels });
    setEditing(true);
  };

  if (collapsed) {
    return (
      <aside className="ws-sidebar is-collapsed">
        <button className="ws-sidebar-toggle" title="show channels" onClick={() => setCollapsed(false)}>
          <PanelLeftOpen size={16} />
        </button>
      </aside>
    );
  }

  return (
    <aside className="ws-sidebar">
      <div className="ws-sidebar-head">
        <input
          className="ws-sidebar-filter"
          placeholder="filter channels…"
          value={filter}
          onChange={(e) => setFilter(e.target.value)}
          data-testid="channel-filter"
        />
        <button className="ws-sidebar-toggle" title="hide channels" onClick={() => setCollapsed(true)}>
          <PanelLeftClose size={16} />
        </button>
      </div>
      {selection.size > 0 && (
        <div className="ws-sidebar-actions">
          <button className="ws-add-chart" onClick={addAsChart} data-testid="add-as-chart">
            <LineChart size={13} /> add as chart ({selection.size})
          </button>
          <button className="ws-clear-sel" onClick={clearSelection}>
            clear
          </button>
        </div>
      )}
      <div className="ws-sidebar-body">
        <ChannelPicker
          devices={filtered}
          selected={selection}
          onToggle={(id) => toggleSelection(channelKey(id.packet, id.name))}
          error={error}
        />
      </div>
    </aside>
  );
}
