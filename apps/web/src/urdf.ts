/**
 * URDF text helpers for the live editor — pure and DOM-only (no three.js), so
 * they parse in jsdom and are unit-tested (urdf.test.ts).
 *
 * The 3D renderer (Scene3D) uses `urdf-loader` to turn URDF text into a
 * three.js object; here we only need to (a) validate that a string is
 * well-formed, parseable URDF and (b) enumerate its joints so the bindings
 * panel can offer a row per joint. Both use the platform `DOMParser` — the same
 * XML parser urdf-loader itself relies on — so a string that validates here is
 * one urdf-loader can consume.
 */

export type JointType =
  | "fixed"
  | "continuous"
  | "revolute"
  | "prismatic"
  | "planar"
  | "floating";

export interface UrdfJoint {
  name: string;
  type: JointType;
}

export interface UrdfParse {
  ok: boolean;
  /** Robot name (`<robot name=...>`) when parseable. */
  robotName?: string;
  joints: UrdfJoint[];
  /** Human-readable message when `ok` is false. */
  error?: string;
}

function getParser(): DOMParser | null {
  if (typeof DOMParser === "undefined") return null;
  return new DOMParser();
}

/**
 * Parse URDF text: validate structure and enumerate joints. Never throws —
 * returns `{ ok:false, error }` on malformed XML or a missing `<robot>` root, so
 * the editor can keep the last-good model on screen and show the message inline.
 */
export function parseUrdf(text: string): UrdfParse {
  const parser = getParser();
  if (!parser) return { ok: false, joints: [], error: "DOMParser unavailable" };

  let doc: Document;
  try {
    doc = parser.parseFromString(text, "application/xml");
  } catch (e) {
    return { ok: false, joints: [], error: e instanceof Error ? e.message : String(e) };
  }

  // Browsers report XML syntax errors as a <parsererror> node rather than
  // throwing. Surface its text (trimmed) as the inline error.
  const err = doc.querySelector("parsererror");
  if (err) {
    const msg = (err.textContent || "XML parse error").replace(/\s+/g, " ").trim();
    return { ok: false, joints: [], error: msg };
  }

  const robot = doc.querySelector("robot");
  if (!robot) {
    return { ok: false, joints: [], error: "no <robot> root element" };
  }

  const joints: UrdfJoint[] = [];
  const seen = new Set<string>();
  for (const j of Array.from(robot.querySelectorAll("joint"))) {
    const name = j.getAttribute("name");
    if (!name || seen.has(name)) continue;
    seen.add(name);
    const type = (j.getAttribute("type") || "fixed") as JointType;
    joints.push({ name, type });
  }

  return {
    ok: true,
    robotName: robot.getAttribute("name") || undefined,
    joints,
  };
}

/** Joints a binding row is useful for (movable joints only). */
export function movableJoints(joints: UrdfJoint[]): UrdfJoint[] {
  return joints.filter((j) => j.type !== "fixed");
}

/**
 * A commented starter URDF: a two-wheel balancer built entirely from primitive
 * geometry (box chassis + two cylinder wheels), needing no mesh files, with two
 * `continuous` wheel joints ready to bind to wheel-velocity/position channels.
 */
export const STARTER_URDF = `<?xml version="1.0"?>
<!-- Gantry starter model: a two-wheel balancer.
     All primitive geometry (box + cylinders) — no mesh files needed.
     Edit dimensions/origins below; the panel re-renders as you type.
     Bind left_wheel_joint / right_wheel_joint to wheel channels in Bindings. -->
<robot name="mr-wobbles">

  <!-- Chassis: a box, its centre lifted to the wheel axle height. -->
  <link name="base_link">
    <visual>
      <origin xyz="0 0 0" rpy="0 0 0"/>
      <geometry>
        <box size="0.40 0.28 0.12"/>
      </geometry>
      <material name="chassis">
        <color rgba="0.31 0.82 0.77 1.0"/>
      </material>
    </visual>
  </link>

  <!-- Left wheel: cylinder rolled onto its side (rpy rolls Z→Y). -->
  <link name="left_wheel">
    <visual>
      <origin xyz="0 0 0" rpy="1.5708 0 0"/>
      <geometry>
        <cylinder radius="0.09" length="0.04"/>
      </geometry>
      <material name="wheel">
        <color rgba="0.14 0.16 0.19 1.0"/>
      </material>
    </visual>
  </link>

  <link name="right_wheel">
    <visual>
      <origin xyz="0 0 0" rpy="1.5708 0 0"/>
      <geometry>
        <cylinder radius="0.09" length="0.04"/>
      </geometry>
      <material name="wheel">
        <color rgba="0.14 0.16 0.19 1.0"/>
      </material>
    </visual>
  </link>

  <!-- Continuous joints spin the wheels about the chassis Y axis. -->
  <joint name="left_wheel_joint" type="continuous">
    <parent link="base_link"/>
    <child link="left_wheel"/>
    <origin xyz="0 0.17 0" rpy="0 0 0"/>
    <axis xyz="0 1 0"/>
  </joint>

  <joint name="right_wheel_joint" type="continuous">
    <parent link="base_link"/>
    <child link="right_wheel"/>
    <origin xyz="0 -0.17 0" rpy="0 0 0"/>
    <axis xyz="0 1 0"/>
  </joint>

</robot>
`;
