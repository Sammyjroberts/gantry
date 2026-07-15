/**
 * Scene3D control surface — the bindings panel + live URDF editor.
 *
 * Deliberately three.js-free (pure React + the pure pose/urdf modules) so it
 * renders in jsdom and is unit-tested directly (scene3dControls.test.tsx),
 * while the sibling Scene3D owns the WebGL canvas and the imperative drive.
 * This component is fully controlled: it renders `bindings`/`urdfText` and
 * reports edits up; Scene3D holds the state and persistence.
 */
import { useEffect, useState } from "react";
import {
  type AngleBinding,
  type JointBinding,
  type ModelKind,
  type OffsetBinding,
  type PoseBindings,
  type PrimitiveDims,
} from "./pose";
import type { UrdfJoint } from "./urdf";

export interface ChannelOption {
  /** Canonical (packet, name) key — the sampler/store key. */
  key: string;
  /** Human label, e.g. "imu.pitch". */
  label: string;
  device: string;
  unit: string;
}

export type SaveState = "idle" | "saving" | "saved" | "error";

export interface ControlsProps {
  device: string;
  devices: string[];
  onDevice: (device: string) => void;

  channels: ChannelOption[];

  bindings: PoseBindings;
  onBindings: (next: PoseBindings) => void;

  modelKind: ModelKind;
  /** Movable joints from the current parse (empty unless a URDF is loaded). */
  joints: UrdfJoint[];

  urdfText: string;
  onUrdfText: (text: string) => void;
  parseError: string | null;
  /** True while a re-parse is pending (debounced), for a subtle "parsing…" hint. */
  parsing: boolean;

  onSave: () => void;
  onLoadFromServer: () => void;
  onNewTemplate: () => void;
  saveState: SaveState;
  saveError: string | null;
  /** URDF editing/saving only applies to a `.urdf` source. */
  editorActive: boolean;
}

/** A channel <select>, with a leading "— none —" option. */
function ChannelSelect({
  value,
  channels,
  onChange,
}: {
  value: string | null;
  channels: ChannelOption[];
  onChange: (key: string | null) => void;
}) {
  return (
    <select
      className="s3-select"
      value={value ?? ""}
      onChange={(e) => onChange(e.target.value === "" ? null : e.target.value)}
    >
      <option value="">— none —</option>
      {channels.map((c) => (
        <option key={c.key} value={c.key}>
          {c.label}
          {c.unit ? ` (${c.unit})` : ""}
        </option>
      ))}
    </select>
  );
}

function UnitToggle({ unit, onToggle }: { unit: "deg" | "rad"; onToggle: () => void }) {
  return (
    <button
      className="s3-chip"
      onClick={onToggle}
      title="toggle channel unit (degrees / radians)"
    >
      {unit}
    </button>
  );
}

function SignToggle({ sign, onToggle }: { sign: 1 | -1; onToggle: () => void }) {
  return (
    <button
      className={`s3-chip ${sign === -1 ? "is-neg" : ""}`}
      onClick={onToggle}
      title="flip sign"
    >
      {sign === -1 ? "−" : "+"}
    </button>
  );
}

function AngleRow({
  label,
  binding,
  channels,
  onChange,
}: {
  label: string;
  binding: AngleBinding;
  channels: ChannelOption[];
  onChange: (next: AngleBinding) => void;
}) {
  return (
    <div className="s3-row">
      <span className="s3-row-label">{label}</span>
      <ChannelSelect
        value={binding.channelKey}
        channels={channels}
        onChange={(channelKey) => onChange({ ...binding, channelKey })}
      />
      <UnitToggle
        unit={binding.unit}
        onToggle={() => onChange({ ...binding, unit: binding.unit === "deg" ? "rad" : "deg" })}
      />
      <SignToggle sign={binding.sign} onToggle={() => onChange({ ...binding, sign: (binding.sign === 1 ? -1 : 1) as 1 | -1 })} />
    </div>
  );
}

function OffsetRow({
  label,
  binding,
  channels,
  onChange,
}: {
  label: string;
  binding: OffsetBinding;
  channels: ChannelOption[];
  onChange: (next: OffsetBinding) => void;
}) {
  return (
    <div className="s3-row">
      <span className="s3-row-label">{label}</span>
      <ChannelSelect
        value={binding.channelKey}
        channels={channels}
        onChange={(channelKey) => onChange({ ...binding, channelKey })}
      />
      <input
        className="s3-num"
        type="number"
        step="0.01"
        value={binding.manual}
        disabled={binding.channelKey !== null}
        title={binding.channelKey ? "driven by channel" : "manual offset (m)"}
        onChange={(e) => onChange({ ...binding, manual: Number(e.target.value) || 0 })}
      />
      <span className="s3-unit">m</span>
    </div>
  );
}

function JointRow({
  joint,
  binding,
  channels,
  onChange,
}: {
  joint: UrdfJoint;
  binding: JointBinding;
  channels: ChannelOption[];
  onChange: (next: JointBinding) => void;
}) {
  const jog = binding.mode === "manual";
  return (
    <div className="s3-jrow">
      <div className="s3-jrow-head">
        <span className="s3-jname" title={`${joint.name} · ${joint.type}`}>
          {joint.name}
        </span>
        <span className="s3-jtype">{joint.type}</span>
        <button
          className={`s3-chip ${jog ? "is-jog" : ""}`}
          onClick={() => onChange({ ...binding, mode: jog ? "channel" : "manual" })}
          title="toggle channel / manual jog"
        >
          {jog ? "jog" : "chan"}
        </button>
      </div>
      {jog ? (
        <div className="s3-row">
          <input
            className="s3-slider"
            type="range"
            min={-Math.PI}
            max={Math.PI}
            step={0.01}
            value={binding.manual}
            onChange={(e) => onChange({ ...binding, manual: Number(e.target.value) })}
          />
          <span className="s3-jval">{binding.manual.toFixed(2)}</span>
        </div>
      ) : (
        <div className="s3-row">
          <ChannelSelect
            value={binding.channelKey}
            channels={channels}
            onChange={(channelKey) => onChange({ ...binding, channelKey })}
          />
          <UnitToggle
            unit={binding.unit}
            onToggle={() => onChange({ ...binding, unit: binding.unit === "deg" ? "rad" : "deg" })}
          />
          <SignToggle
            sign={binding.sign}
            onToggle={() => onChange({ ...binding, sign: (binding.sign === 1 ? -1 : 1) as 1 | -1 })}
          />
        </div>
      )}
    </div>
  );
}

const DIM_FIELDS: ReadonlyArray<{ key: keyof PrimitiveDims; label: string }> = [
  { key: "chassisLen", label: "chassis L" },
  { key: "chassisWidth", label: "chassis W" },
  { key: "chassisHeight", label: "chassis H" },
  { key: "wheelRadius", label: "wheel r" },
  { key: "wheelWidth", label: "wheel w" },
  { key: "trackWidth", label: "track" },
];

export function Scene3DControls(props: ControlsProps) {
  const {
    device,
    devices,
    onDevice,
    channels,
    bindings,
    onBindings,
    modelKind,
    joints,
    urdfText,
    onUrdfText,
    parseError,
    parsing,
    onSave,
    onLoadFromServer,
    onNewTemplate,
    saveState,
    saveError,
    editorActive,
  } = props;

  const set = (patch: Partial<PoseBindings>) => onBindings({ ...bindings, ...patch });
  const setJoint = (name: string, jb: JointBinding) =>
    onBindings({ ...bindings, joints: { ...bindings.joints, [name]: jb } });
  const setDim = (key: keyof PrimitiveDims, v: number) =>
    onBindings({ ...bindings, dims: { ...bindings.dims, [key]: v } });

  const jointBinding = (name: string): JointBinding =>
    bindings.joints[name] ?? { mode: "channel", channelKey: null, unit: "rad", sign: 1, manual: 0 };

  return (
    <div className="s3-controls">
      <div className="s3-section">
        <div className="s3-section-head">Attitude</div>
        <AngleRow label="pitch" binding={bindings.pitch} channels={channels} onChange={(pitch) => set({ pitch })} />
        <AngleRow label="roll" binding={bindings.roll} channels={channels} onChange={(roll) => set({ roll })} />
        <AngleRow label="yaw" binding={bindings.yaw} channels={channels} onChange={(yaw) => set({ yaw })} />
      </div>

      <div className="s3-section">
        <div className="s3-section-head">Offset</div>
        <OffsetRow label="x" binding={bindings.x} channels={channels} onChange={(x) => set({ x })} />
        <OffsetRow label="y" binding={bindings.y} channels={channels} onChange={(y) => set({ y })} />
        <OffsetRow label="z" binding={bindings.z} channels={channels} onChange={(z) => set({ z })} />
      </div>

      {joints.length > 0 && (
        <div className="s3-section">
          <div className="s3-section-head">
            Joints <span className="s3-count">{joints.length}</span>
          </div>
          {joints.map((j) => (
            <JointRow
              key={j.name}
              joint={j}
              binding={jointBinding(j.name)}
              channels={channels}
              onChange={(jb) => setJoint(j.name, jb)}
            />
          ))}
        </div>
      )}

      {modelKind === "primitive" && (
        <div className="s3-section">
          <div className="s3-section-head">Primitive dims</div>
          <div className="s3-dims">
            {DIM_FIELDS.map((f) => (
              <label className="s3-dim" key={f.key}>
                <span>{f.label}</span>
                <input
                  className="s3-num"
                  type="number"
                  step="0.01"
                  min={0.01}
                  value={bindings.dims[f.key]}
                  onChange={(e) => setDim(f.key, Math.max(0.001, Number(e.target.value) || 0))}
                />
              </label>
            ))}
          </div>
        </div>
      )}

      <div className="s3-section s3-editor">
        <div className="s3-editor-head">
          <span className="s3-section-head">URDF</span>
          <span className={`s3-src s3-src--${modelKind}`} title="active render source">
            {modelKind}
          </span>
          {devices.length > 0 && (
            <select
              className="s3-select s3-device"
              value={device}
              onChange={(e) => onDevice(e.target.value)}
              title="device (model + bindings)"
            >
              {devices.map((d) => (
                <option key={d} value={d}>
                  {d}
                </option>
              ))}
            </select>
          )}
        </div>

        <div className="s3-editor-actions">
          <button className="s3-btn" onClick={onLoadFromServer} title="reload URDF from server">
            load
          </button>
          <button
            className="s3-btn s3-btn--save"
            onClick={onSave}
            disabled={!editorActive || saveState === "saving"}
            title={editorActive ? "PUT /models/<device>.urdf" : "save applies to a .urdf source"}
          >
            {saveState === "saving" ? "saving…" : saveState === "saved" ? "saved ✓" : "save"}
          </button>
          <button className="s3-btn" onClick={onNewTemplate} title="replace with a starter URDF">
            new template
          </button>
          <span className="s3-parse-state">
            {parsing ? "parsing…" : parseError ? "" : "ok"}
          </span>
        </div>

        <textarea
          className="s3-textarea"
          spellCheck={false}
          wrap="off"
          value={urdfText}
          onChange={(e) => onUrdfText(e.target.value)}
          placeholder="URDF XML — edit to re-render live"
        />

        {parseError && <div className="s3-parse-err">⚠ {parseError}</div>}
        {saveError && saveState === "error" && (
          <div className="s3-parse-err">⚠ save failed: {saveError}</div>
        )}
        <div className="s3-hint">
          angles → radians · pitch/roll/yaw signed · edits re-render (300ms) · last
          good model stays on error
        </div>
      </div>
    </div>
  );
}

/**
 * Debounce a value by `ms`: returns the input, but only after it has been
 * stable for `ms`. Scene3D uses this to gate URDF re-parse so keystrokes don't
 * thrash the loader. Three-free, so it lives here for reuse and testing.
 */
export function useDebounced<T>(value: T, ms: number): T {
  const [debounced, setDebounced] = useState(value);
  useEffect(() => {
    const id = setTimeout(() => setDebounced(value), ms);
    return () => clearTimeout(id);
  }, [value, ms]);
  return debounced;
}
