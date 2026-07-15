import { describe, it, expect, afterEach, vi } from "vitest";
import { useState } from "react";
import { render, screen, fireEvent, cleanup } from "@testing-library/react";
import { Scene3DControls, type ChannelOption } from "./scene3dControls";
import { defaultBindings, type PoseBindings } from "./pose";
import { STARTER_URDF, parseUrdf, movableJoints, type UrdfJoint } from "./urdf";

afterEach(() => cleanup());

const CHANNELS: ChannelOption[] = [
  { key: "imupitch", label: "imu.pitch", device: "mr-wobbles", unit: "deg" },
  { key: "encleft", label: "enc.left", device: "mr-wobbles", unit: "rad" },
];

/** Stateful harness: mirrors how Scene3D drives this controlled component. */
function Harness({
  joints = [],
  modelKind = "urdf" as const,
  onSave = () => {},
  parseError = null as string | null,
}: {
  joints?: UrdfJoint[];
  modelKind?: "urdf" | "primitive" | "glb" | "stl";
  onSave?: () => void;
  parseError?: string | null;
}) {
  const [bindings, setBindings] = useState<PoseBindings>(defaultBindings());
  const [urdfText, setUrdfText] = useState(STARTER_URDF);
  return (
    <Scene3DControls
      device="mr-wobbles"
      devices={["mr-wobbles"]}
      onDevice={() => {}}
      channels={CHANNELS}
      bindings={bindings}
      onBindings={setBindings}
      modelKind={modelKind}
      joints={joints}
      urdfText={urdfText}
      onUrdfText={setUrdfText}
      parseError={parseError}
      parsing={false}
      onSave={onSave}
      onLoadFromServer={() => {}}
      onNewTemplate={() => setUrdfText(STARTER_URDF)}
      saveState="idle"
      saveError={null}
      editorActive={modelKind === "urdf" || modelKind === "primitive"}
    />
  );
}

describe("Scene3DControls", () => {
  it("renders the attitude, offset and URDF sections", () => {
    render(<Harness />);
    expect(screen.getByText("Attitude")).toBeTruthy();
    expect(screen.getByText("Offset")).toBeTruthy();
    expect(screen.getByText("pitch")).toBeTruthy();
    expect(screen.getByText("roll")).toBeTruthy();
    expect(screen.getByText("yaw")).toBeTruthy();
    // Editor textarea seeded with the current URDF text.
    const ta = screen.getByPlaceholderText(/URDF XML/i) as HTMLTextAreaElement;
    expect(ta.value).toContain("<robot");
  });

  it("shows a channel option in the attitude pickers", () => {
    render(<Harness />);
    expect(screen.getAllByText("imu.pitch (deg)").length).toBeGreaterThan(0);
  });

  it("flips the pitch sign when the sign chip is clicked", () => {
    render(<Harness />);
    // The first sign chip belongs to the pitch row.
    const signChip = screen.getAllByTitle("flip sign")[0]!;
    expect(signChip.textContent).toBe("+");
    fireEvent.click(signChip);
    expect(signChip.textContent).toBe("−");
  });

  it("renders a joint row per movable joint and toggles jog mode", () => {
    const joints = movableJoints(parseUrdf(STARTER_URDF).joints);
    render(<Harness joints={joints} />);
    expect(screen.getByText("left_wheel_joint")).toBeTruthy();
    expect(screen.getByText("right_wheel_joint")).toBeTruthy();
    // The Joints section header carries the count.
    expect(screen.getByText("Joints")).toBeTruthy();

    const modeChips = screen.getAllByTitle("toggle channel / manual jog");
    expect(modeChips[0]!.textContent).toBe("chan");
    fireEvent.click(modeChips[0]!);
    expect(modeChips[0]!.textContent).toBe("jog");
  });

  it("shows the primitive dimension editor only in primitive mode", () => {
    const { rerender } = render(<Harness modelKind="primitive" />);
    expect(screen.getByText("Primitive dims")).toBeTruthy();
    rerender(<Harness modelKind="urdf" />);
    expect(screen.queryByText("Primitive dims")).toBeNull();
  });

  it("surfaces a parse error inline", () => {
    render(<Harness parseError="unexpected end of input" />);
    expect(screen.getByText(/unexpected end of input/)).toBeTruthy();
  });

  it("disables Save unless the source is editable, and fires it otherwise", () => {
    const onSave = vi.fn();
    const { rerender } = render(<Harness modelKind="glb" onSave={onSave} />);
    const saveBtn = screen.getByTitle(/save applies to a .urdf source/i) as HTMLButtonElement;
    expect(saveBtn.disabled).toBe(true);

    rerender(<Harness modelKind="urdf" onSave={onSave} />);
    fireEvent.click(screen.getByText("save"));
    expect(onSave).toHaveBeenCalled();
  });

  it("edits the URDF text through the textarea", () => {
    render(<Harness />);
    const ta = screen.getByPlaceholderText(/URDF XML/i) as HTMLTextAreaElement;
    fireEvent.change(ta, { target: { value: '<robot name="x"/>' } });
    expect(ta.value).toBe('<robot name="x"/>');
  });
});
