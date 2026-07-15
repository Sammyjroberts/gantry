/**
 * Scene3D — the lazy-loaded 3D robot viewer + live URDF editor.
 *
 * This module is the ONLY place three.js / @react-three / urdf-loader are
 * imported, and App pulls it in exclusively via `React.lazy(() =>
 * import("./Scene3D"))`. That keeps every three.js byte out of the main bundle:
 * the whole feature is a separate chunk fetched only when the "3D" toggle is on
 * (verify in the Vite build output — the three/drei chunk is emitted lazily).
 *
 * Responsibilities:
 *  - Resolve a model per device (urdf → glb → stl → generated primitive) from
 *    the server's /models listing (see pose.resolveModelSource, models.ts).
 *  - Render a dark grid scene with soft lighting, orbit controls, an axes gizmo
 *    and a contact shadow; keep it cheap (no dynamic shadow maps).
 *  - Drive the model imperatively each frame from a `Sampler` ref (live ring
 *    values, or the replay cursor value) — no React re-render per frame.
 *  - Host the live URDF editor: debounced re-parse+re-render, inline errors,
 *    last-good model retained on failure, Save (PUT), Load, new-from-template.
 *
 * The control surface (bindings + editor) lives in the three-free
 * scene3dControls module so it is unit-tested without a WebGL context.
 */
import { Suspense, useEffect, useMemo, useRef, useState, type MutableRefObject } from "react";
import * as THREE from "three";
import { Canvas, useFrame } from "@react-three/fiber";
import { OrbitControls, Grid, ContactShadows, GizmoHelper, GizmoViewport } from "@react-three/drei";
import URDFLoader from "urdf-loader";
import type { URDFRobot } from "urdf-loader";
import { STLLoader } from "three/examples/jsm/loaders/STLLoader.js";
import { GLTFLoader } from "three/examples/jsm/loaders/GLTFLoader.js";
import {
  boundChannelKeys,
  defaultBindings,
  resolveAngle,
  resolveJoint,
  resolveOffset,
  resolveModelSource,
  type ModelKind,
  type PoseBindings,
  type PrimitiveDims,
  type Sampler,
} from "./pose";
import { encodeVizConfig } from "./hardware";
import { movableJoints, parseUrdf, STARTER_URDF, type UrdfJoint } from "./urdf";
import { listModels, loadModelText, modelUrl, saveModelText } from "./models";
import {
  Scene3DControls,
  useDebounced,
  type ChannelOption,
  type SaveState,
} from "./scene3dControls";

export interface Scene3DProps {
  baseUrl: string;
  /** Device ids from the catalogue (for the model/binding selector). */
  devices: string[];
  /** All catalogue channels, offered in the binding pickers. */
  channels: ChannelOption[];
  /**
   * Fresh sampler each render: live → latest ring value; replay → value at the
   * playback cursor. Read imperatively in the frame loop (never re-renders).
   */
  sampleRef: MutableRefObject<Sampler>;
  /** True while an experiment replay is active (label only). */
  replaying: boolean;
  /** Report the channel keys the current bindings need, so App can subscribe. */
  onBoundChannelsChange: (keys: string[]) => void;
  /**
   * Load a device's pose bindings from the server (HardwareService
   * viz_config_json), migrating any legacy localStorage copy once. Durable viz
   * state lives server-side now — never browser-local.
   */
  loadVizConfig: (device: string) => Promise<PoseBindings>;
  /** Persist a device's pose bindings to the server (debounced by the caller). */
  saveVizConfig: (device: string, bindings: PoseBindings) => Promise<void>;
  onClose: () => void;
}

type RenderMode = ModelKind;

/** Build the generated primitive robot: box chassis + two cylinder wheels. */
function buildPrimitive(dims: PrimitiveDims): THREE.Group {
  const g = new THREE.Group();
  g.name = "primitive-robot";
  const r = dims.wheelRadius;

  const chassisMat = new THREE.MeshStandardMaterial({
    color: 0x4fd1c5,
    metalness: 0.1,
    roughness: 0.6,
  });
  const wheelMat = new THREE.MeshStandardMaterial({
    color: 0x24282f,
    metalness: 0.2,
    roughness: 0.8,
  });

  const chassis = new THREE.Mesh(
    new THREE.BoxGeometry(dims.chassisLen, dims.chassisHeight, dims.chassisWidth),
    chassisMat,
  );
  chassis.position.set(0, r, 0);
  g.add(chassis);

  const wheelGeo = new THREE.CylinderGeometry(r, r, dims.wheelWidth, 24);
  for (const side of [1, -1]) {
    const w = new THREE.Mesh(wheelGeo, wheelMat);
    // Cylinder axis is Y by default; rotate onto Z (the track direction).
    w.rotation.x = Math.PI / 2;
    w.position.set(0, r, (side * dims.trackWidth) / 2);
    g.add(w);
  }
  return g;
}

/** Parse URDF text into a three object (ROS Z-up rotated onto three Y-up). */
function buildUrdf(text: string, baseUrl: string): URDFRobot {
  const loader = new URDFLoader();
  loader.workingPath = modelUrl(baseUrl); // resolves mesh refs to /models/
  loader.parseCollision = false;
  const robot = loader.parse(text);
  robot.rotation.x = -Math.PI / 2;
  return robot;
}

function disposeObject(obj: THREE.Object3D | null): void {
  if (!obj) return;
  obj.traverse((o) => {
    const m = o as THREE.Mesh;
    if (m.geometry) m.geometry.dispose();
    const mat = m.material as THREE.Material | THREE.Material[] | undefined;
    if (Array.isArray(mat)) mat.forEach((x) => x.dispose());
    else mat?.dispose();
  });
}

/**
 * The driven rig: places `object` under a pose group and, every frame, reads
 * the sampler to set body attitude (pitch/roll/yaw), offset, and — for a URDF
 * robot — each joint. Runs inside the Canvas so `useFrame` is available. Reads
 * live props via a ref so a bindings edit takes effect without remounting.
 */
function RobotRig({
  object,
  bindings,
  joints,
  sampleRef,
}: {
  object: THREE.Object3D | null;
  bindings: PoseBindings;
  joints: UrdfJoint[];
  sampleRef: MutableRefObject<Sampler>;
}) {
  const groupRef = useRef<THREE.Group>(null);
  // Latest bindings/joints for the frame loop without re-subscribing each edit.
  const live = useRef({ bindings, joints, object });
  live.current = { bindings, joints, object };

  useFrame(() => {
    const g = groupRef.current;
    if (!g) return;
    const s = sampleRef.current;
    const { bindings: b, joints: js, object: obj } = live.current;

    g.rotation.order = "YXZ";
    g.rotation.set(
      resolveAngle(b.pitch, s), // three X
      resolveAngle(b.yaw, s), // three Y (up)
      resolveAngle(b.roll, s), // three Z
    );
    // x→forward(X), y→lateral(Z), z→up(Y).
    g.position.set(resolveOffset(b.x, s), resolveOffset(b.z, s), resolveOffset(b.y, s));

    const robot = obj as URDFRobot | null;
    if (robot && (robot as { isURDFRobot?: boolean }).isURDFRobot) {
      for (const j of js) {
        const jb = b.joints[j.name];
        if (jb) robot.setJointValue(j.name, resolveJoint(jb, s));
      }
    }
  });

  return <group ref={groupRef}>{object && <primitive object={object} />}</group>;
}

function SceneContents({
  object,
  bindings,
  joints,
  sampleRef,
}: {
  object: THREE.Object3D | null;
  bindings: PoseBindings;
  joints: UrdfJoint[];
  sampleRef: MutableRefObject<Sampler>;
}) {
  return (
    <>
      <color attach="background" args={["#0b0e11"]} />
      <hemisphereLight args={[0xbfd4e6, 0x1a1f26, 0.9]} />
      <directionalLight position={[3, 5, 2]} intensity={1.1} />
      <Grid
        args={[10, 10]}
        cellSize={0.1}
        cellColor="#232a31"
        sectionSize={0.5}
        sectionColor="#33414c"
        fadeDistance={9}
        fadeStrength={1.5}
        infiniteGrid
        position={[0, 0, 0]}
      />
      <ContactShadows position={[0, 0.001, 0]} opacity={0.45} scale={4} blur={2.5} far={2} />
      <RobotRig object={object} bindings={bindings} joints={joints} sampleRef={sampleRef} />
      <axesHelper args={[0.25]} />
      <OrbitControls makeDefault enableDamping target={[0, 0.12, 0]} minDistance={0.2} maxDistance={8} />
      <GizmoHelper alignment="bottom-right" margin={[56, 56]}>
        <GizmoViewport axisColors={["#e5484d", "#3fb950", "#7aa2f7"]} labelColor="#c7d0d9" />
      </GizmoHelper>
    </>
  );
}

export default function Scene3D(props: Scene3DProps) {
  const {
    baseUrl,
    devices,
    channels,
    sampleRef,
    replaying,
    onBoundChannelsChange,
    loadVizConfig,
    saveVizConfig,
    onClose,
  } = props;

  const [device, setDevice] = useState<string>(devices[0] ?? "robot");
  // Bindings start at defaults and are loaded from the server per-device below.
  const [bindings, setBindings] = useState<PoseBindings>(() => defaultBindings());
  const [renderMode, setRenderMode] = useState<RenderMode>("primitive");
  const [urdfText, setUrdfText] = useState<string>(STARTER_URDF);
  const [meshObject, setMeshObject] = useState<THREE.Object3D | null>(null);
  const [saveState, setSaveState] = useState<SaveState>("idle");
  const [saveError, setSaveError] = useState<string | null>(null);
  const [buildError, setBuildError] = useState<string | null>(null);

  // Debounced editor text drives the URDF re-parse/re-render (300ms).
  const debouncedText = useDebounced(urdfText, 300);
  const parse = useMemo(() => parseUrdf(debouncedText), [debouncedText]);
  const joints = useMemo(
    () => (renderMode === "urdf" ? movableJoints(parse.joints) : []),
    [renderMode, parse],
  );

  // Last successfully-built URDF object — retained so a parse/build failure
  // never blanks the viewer (the editor shows the error, the model stays).
  const [urdfObject, setUrdfObject] = useState<THREE.Object3D | null>(null);
  const lastGoodUrdf = useRef<URDFRobot | null>(null);
  useEffect(() => {
    if (renderMode !== "urdf") return;
    if (!parse.ok) return; // keep the last good model; parseError is shown inline
    try {
      const robot = buildUrdf(debouncedText, baseUrl);
      const prev = lastGoodUrdf.current;
      lastGoodUrdf.current = robot;
      setUrdfObject(robot);
      setBuildError(null);
      if (prev && prev !== robot) disposeObject(prev);
    } catch (e) {
      setBuildError(e instanceof Error ? e.message : String(e));
    }
  }, [renderMode, parse.ok, debouncedText, baseUrl]);

  // Generated primitive, rebuilt on dimension edits.
  const dims = bindings.dims;
  const primitiveObject = useMemo(
    () => buildPrimitive(dims),
    // rebuild when any dimension changes
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [dims.chassisLen, dims.chassisWidth, dims.chassisHeight, dims.wheelRadius, dims.wheelWidth, dims.trackWidth],
  );

  const activeObject: THREE.Object3D | null =
    renderMode === "urdf" ? urdfObject : renderMode === "primitive" ? primitiveObject : meshObject;

  const parseError = renderMode === "urdf" ? parse.error ?? buildError ?? null : null;

  // ---- per-device init: resolve the model source --------------------------
  useEffect(() => {
    setSaveState("idle");
    setSaveError(null);
    const ac = new AbortController();
    void (async () => {
      let files: string[] = [];
      try {
        files = await listModels(baseUrl, ac.signal);
      } catch {
        files = []; // no listing → primitive fallback
      }
      if (ac.signal.aborted) return;
      const src = resolveModelSource(device, files);
      if (src.kind === "urdf" && src.file) {
        try {
          const text = await loadModelText(baseUrl, src.file, ac.signal);
          if (ac.signal.aborted) return;
          setUrdfText(text);
          setRenderMode("urdf");
          return;
        } catch {
          if (ac.signal.aborted) return;
        }
      }
      if ((src.kind === "glb" || src.kind === "stl") && src.file) {
        setRenderMode(src.kind);
        return;
      }
      // Fallback: generated primitive (editor seeded with a starter template).
      setRenderMode("primitive");
      setUrdfText(STARTER_URDF);
    })();
    return () => ac.abort();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [device, baseUrl]);

  // ---- async mesh load for glb/stl sources --------------------------------
  useEffect(() => {
    if (renderMode !== "glb" && renderMode !== "stl") return;
    const ac = new AbortController();
    const url = modelUrl(baseUrl, `${device}.${renderMode}`);
    let disposed: THREE.Object3D | null = null;
    void (async () => {
      try {
        if (renderMode === "glb") {
          const gltf = await new GLTFLoader().loadAsync(url);
          if (ac.signal.aborted) return;
          disposed = gltf.scene;
          setMeshObject(gltf.scene);
        } else {
          const geo = await new STLLoader().loadAsync(url);
          if (ac.signal.aborted) return;
          const mesh = new THREE.Mesh(
            geo,
            new THREE.MeshStandardMaterial({ color: 0x8aa0b4, metalness: 0.2, roughness: 0.7 }),
          );
          disposed = mesh;
          setMeshObject(mesh);
        }
      } catch (e) {
        if (!ac.signal.aborted) setBuildError(e instanceof Error ? e.message : String(e));
      }
    })();
    return () => {
      ac.abort();
      disposeObject(disposed);
    };
  }, [renderMode, device, baseUrl]);

  // Tracks which device the current `bindings` were loaded for, and the last
  // viz JSON we loaded/saved — so the debounced save fires only on a genuine
  // change and never echoes the value we just loaded (which would be a
  // redundant write, or worse, clobber during a device switch).
  const loadedDeviceRef = useRef<string | null>(null);
  const lastVizJsonRef = useRef<string>("");

  // ---- load bindings from the server on device change ---------------------
  useEffect(() => {
    let cancelled = false;
    loadedDeviceRef.current = null;
    void loadVizConfig(device).then((b) => {
      if (cancelled) return;
      setBindings(b);
      loadedDeviceRef.current = device;
      lastVizJsonRef.current = encodeVizConfig(b);
    });
    return () => {
      cancelled = true;
    };
  }, [device, loadVizConfig]);

  // Report bound channels up immediately (no debounce) so App's subscription
  // tracks edits without waiting on the save.
  useEffect(() => {
    onBoundChannelsChange(boundChannelKeys(bindings));
  }, [bindings, onBoundChannelsChange]);

  // ---- debounced (~1s) server save on change ------------------------------
  const debouncedBindings = useDebounced(bindings, 1000);
  useEffect(() => {
    // Only save bindings that belong to the currently-loaded device.
    if (loadedDeviceRef.current !== device) return;
    const json = encodeVizConfig(debouncedBindings);
    if (json === lastVizJsonRef.current) return; // no change vs loaded/last-saved
    lastVizJsonRef.current = json;
    void saveVizConfig(device, debouncedBindings);
  }, [debouncedBindings, device, saveVizConfig]);

  // Clear reported channels on unmount so App drops the extra subscriptions.
  useEffect(() => () => onBoundChannelsChange([]), [onBoundChannelsChange]);

  // ---- editor actions -----------------------------------------------------
  const onLoadFromServer = () => {
    const ac = new AbortController();
    void (async () => {
      try {
        const text = await loadModelText(baseUrl, `${device}.urdf`, ac.signal);
        setUrdfText(text);
        setRenderMode("urdf");
        setSaveState("idle");
      } catch (e) {
        setSaveError(e instanceof Error ? e.message : String(e));
        setSaveState("error");
      }
    })();
  };

  const onNewTemplate = () => {
    setUrdfText(STARTER_URDF);
    setRenderMode("urdf");
    setSaveState("idle");
    setSaveError(null);
  };

  const onSave = () => {
    setSaveState("saving");
    setSaveError(null);
    void (async () => {
      try {
        await saveModelText(baseUrl, `${device}.urdf`, urdfText);
        setSaveState("saved");
        setTimeout(() => setSaveState((s) => (s === "saved" ? "idle" : s)), 1800);
      } catch (e) {
        setSaveError(e instanceof Error ? e.message : String(e));
        setSaveState("error");
      }
    })();
  };

  const parsing = urdfText !== debouncedText;
  const label = device || "robot";

  return (
    <div className="scene3d">
      <div className="scene3d-viewport">
        <div className="scene3d-label">
          <span className="scene3d-label-name">{label}</span>
          <span className={`scene3d-label-mode s3-src--${renderMode}`}>{renderMode}</span>
          {replaying && <span className="scene3d-label-replay">▶ replay</span>}
        </div>
        <button className="scene3d-close" onClick={onClose} title="close 3D panel">
          ✕
        </button>
        <Suspense fallback={<div className="scene3d-loading">initializing renderer…</div>}>
          <Canvas
            dpr={[1, 2]}
            gl={{ antialias: true, powerPreference: "high-performance" }}
            camera={{ position: [0.7, 0.55, 0.9], fov: 45, near: 0.01, far: 100 }}
          >
            <SceneContents
              object={activeObject}
              bindings={bindings}
              joints={joints}
              sampleRef={sampleRef}
            />
          </Canvas>
        </Suspense>
      </div>

      <Scene3DControls
        device={device}
        devices={devices}
        onDevice={setDevice}
        channels={channels}
        bindings={bindings}
        onBindings={setBindings}
        modelKind={renderMode}
        joints={joints}
        urdfText={urdfText}
        onUrdfText={setUrdfText}
        parseError={parseError}
        parsing={parsing}
        onSave={onSave}
        onLoadFromServer={onLoadFromServer}
        onNewTemplate={onNewTemplate}
        saveState={saveState}
        saveError={saveError}
        editorActive={renderMode === "urdf" || renderMode === "primitive"}
      />
    </div>
  );
}
