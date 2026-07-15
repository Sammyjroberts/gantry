import { describe, it, expect } from "vitest";
import { parseUrdf, movableJoints, STARTER_URDF } from "./urdf";

describe("parseUrdf", () => {
  it("parses the starter template and finds its two wheel joints", () => {
    const p = parseUrdf(STARTER_URDF);
    expect(p.ok).toBe(true);
    expect(p.robotName).toBe("mr-wobbles");
    const names = p.joints.map((j) => j.name).sort();
    expect(names).toEqual(["left_wheel_joint", "right_wheel_joint"]);
    expect(p.joints.every((j) => j.type === "continuous")).toBe(true);
  });

  it("reports well-formed XML that is not a robot", () => {
    const p = parseUrdf('<?xml version="1.0"?><notrobot/>');
    expect(p.ok).toBe(false);
    expect(p.error).toMatch(/robot/i);
  });

  it("catches malformed XML without throwing (surfaces a message)", () => {
    const p = parseUrdf("<robot><link></robot>"); // mismatched tags
    expect(p.ok).toBe(false);
    expect(typeof p.error).toBe("string");
    expect(p.error!.length).toBeGreaterThan(0);
  });

  it("enumerates mixed joint types and de-duplicates by name", () => {
    const xml = `<robot name="r">
      <joint name="a" type="revolute"/>
      <joint name="b" type="fixed"/>
      <joint name="a" type="prismatic"/>
    </robot>`;
    const p = parseUrdf(xml);
    expect(p.ok).toBe(true);
    expect(p.joints.map((j) => j.name)).toEqual(["a", "b"]);
    expect(p.joints[0]!.type).toBe("revolute"); // first wins
  });

  it("defaults a joint with no type attribute to fixed", () => {
    const p = parseUrdf('<robot name="r"><joint name="j"/></robot>');
    expect(p.joints[0]).toEqual({ name: "j", type: "fixed" });
  });
});

describe("movableJoints", () => {
  it("drops fixed joints (no binding row for them)", () => {
    const p = parseUrdf(`<robot name="r">
      <joint name="hinge" type="revolute"/>
      <joint name="weld" type="fixed"/>
    </robot>`);
    expect(movableJoints(p.joints).map((j) => j.name)).toEqual(["hinge"]);
  });
});
