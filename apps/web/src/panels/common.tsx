import { parseKey, channelKey, channelLabel } from "../channel";
import type { PanelBinding, Panel } from "../workspace/layout";
import type { ChannelOption } from "../scene3dControls";

/** Human label for a binding: `packet.channel` (bare channel for ad-hoc). */
export function bindingLabel(b: PanelBinding | null): string {
  if (!b) return "unbound";
  return channelLabel(b.packet, b.channel);
}

/** The derived (auto) title for a panel from its config, when no custom title. */
export function autoTitle(panel: Panel): string {
  switch (panel.type) {
    case "timeseries": {
      const chans = (panel.config as { channels: PanelBinding[] }).channels;
      if (chans.length === 0) return "chart";
      if (chans.length === 1) return bindingLabel(chans[0]!);
      return `${bindingLabel(chans[0]!)} +${chans.length - 1}`;
    }
    case "state":
    case "led":
    case "value": {
      const c = (panel.config as { channel: PanelBinding | null }).channel;
      return bindingLabel(c);
    }
    case "scene3d":
      return (panel.config as { deviceId: string }).deviceId || "3D";
    case "video":
      return (panel.config as { cameraId: string }).cameraId || "camera";
    case "sql":
      return "sql";
  }
}

/** Build a {@link PanelBinding} from a picked catalogue option. */
export function bindingFromOption(opt: ChannelOption): PanelBinding {
  const id = parseKey(opt.key);
  return { deviceId: opt.device, packet: id.packet, channel: id.name };
}

/** A single-channel binding picker (used by state/led/value config editors). */
export function BindingPicker({
  value,
  options,
  onChange,
  allowClear = true,
}: {
  value: PanelBinding | null;
  options: ChannelOption[];
  onChange: (binding: PanelBinding | null) => void;
  allowClear?: boolean;
}) {
  const current = value ? channelKey(value.packet, value.channel) : "";
  return (
    <select
      className="panel-cfg-select"
      value={current}
      onChange={(e) => {
        const key = e.target.value;
        if (!key) return onChange(null);
        const opt = options.find((o) => o.key === key);
        onChange(opt ? bindingFromOption(opt) : null);
      }}
    >
      {allowClear && <option value="">— none —</option>}
      {value && !options.some((o) => o.key === current) && (
        <option value={current}>{bindingLabel(value)} (unresolved)</option>
      )}
      {options.map((o) => (
        <option key={o.key} value={o.key}>
          {o.label}
        </option>
      ))}
    </select>
  );
}

/** The explicit "unresolved binding" state shown when a bind is absent. */
export function UnresolvedBinding({
  binding,
  options,
  onRebind,
}: {
  binding: PanelBinding | null;
  options: ChannelOption[];
  onRebind: (binding: PanelBinding | null) => void;
}) {
  return (
    <div className="panel-unresolved">
      <div className="panel-unresolved-msg">
        {binding ? (
          <>
            unresolved binding
            <code>{bindingLabel(binding)}</code>
            {binding.deviceId && <span className="panel-unresolved-dev">on {binding.deviceId}</span>}
          </>
        ) : (
          "no channel bound"
        )}
      </div>
      <div className="panel-unresolved-rebind">
        <span>rebind:</span>
        <BindingPicker value={binding} options={options} onChange={onRebind} />
      </div>
    </div>
  );
}
