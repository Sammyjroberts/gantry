#!/usr/bin/env python3
"""Make a Gantry bench SO-101-native in one command.

Downloads + verifies the SO-101 meshes (pinned in model/MANIFEST.json), uploads
them and the URDF to the bench's /models/ store, configures both arm devices
with a 3D visualization binding per joint, and creates a "SO-101 Teleop"
workspace. Safe to re-run: uploads overwrite, hardware config is upserted, and
the workspace is skipped if one already exists by name.

    python so101_setup.py                         # local bench, localhost:4780
    python so101_setup.py --endpoint http://bench-host:4780 --token gtk_...
    python so101_setup.py --dry-run               # show the plan, touch nothing

Stdlib only (urllib) — no pip install. Meshes are cached under model/.cache
(gitignored) so a second run is fully offline.

Joint telemetry is bound to the "pos" channel, which BOTH bridge backends emit
(the lerobot backend as calibrated degrees, the raw backend as raw-center
degrees). --pos-channel exists only for custom emitters using another name.
"""

import argparse
import hashlib
import json
import os
import sys
import urllib.error
import urllib.request

HERE = os.path.dirname(os.path.abspath(__file__))
MODEL_DIR = os.path.join(HERE, "model")
MANIFEST_PATH = os.path.join(MODEL_DIR, "MANIFEST.json")

# Canonical (packet, name) channel key separator — U+001F, matching the console's
# channelKey() in apps/web/src/channel.ts. A joint binding references its
# telemetry channel by this exact key.
SEP = "\u001f"

# URDF joint name -> telemetry packet name. For the SO-101 they are identical
# (verified against the shipped so101.urdf), so this is 1:1; kept explicit so a
# future URDF with different joint names only needs this table edited.
JOINTS = ["shoulder_pan", "shoulder_lift", "elbow_flex", "wrist_flex", "wrist_roll", "gripper"]

DEVICES = [
    ("so101-leader", "SO-101 Leader", "SO-101 leader arm (teleoperation input) — 6x Feetech STS3215."),
    ("so101-follower", "SO-101 Follower", "SO-101 follower arm (teleoperation output) — 6x Feetech STS3215."),
]

WORKSPACE_NAME = "SO-101 Teleop"

# Key joints paired leader-vs-follower in the workspace charts (teleop tracking).
CHART_JOINTS = ["shoulder_pan", "shoulder_lift", "elbow_flex", "wrist_flex"]
# Joint whose temperature stands in for each arm's thermal health in a value
# panel (value panels bind a single channel — see caveat in the report/README).
TEMP_JOINT = "shoulder_lift"


# ---------------------------------------------------------------------------
# tiny CLI chrome
# ---------------------------------------------------------------------------

def step(msg):
    print(f"  -> {msg}")


def ok(msg):
    print(f"  [ok] {msg}")


def fail(msg):
    print(f"  [!!] {msg}", file=sys.stderr)


# ---------------------------------------------------------------------------
# HTTP (plain urllib against the bench's ConnectRPC JSON + /models/ endpoints)
# ---------------------------------------------------------------------------

class Bench:
    def __init__(self, endpoint, token, dry_run):
        self.endpoint = endpoint.rstrip("/")
        self.token = token
        self.dry_run = dry_run

    def _headers(self, extra=None):
        h = {}
        if self.token:
            h["authorization"] = f"Bearer {self.token}"
        if extra:
            h.update(extra)
        return h

    def _do(self, req, tolerate=()):
        """Send req. Returns (status, body). HTTP error codes in `tolerate` are
        returned rather than raised (e.g. 404 for a not-yet-configured device)."""
        try:
            with urllib.request.urlopen(req, timeout=30) as resp:
                return resp.status, resp.read()
        except urllib.error.HTTPError as e:
            body = e.read()
            if e.code in tolerate:
                return e.code, body
            if e.code in (401, 403):
                hint = "" if self.token else " (no --token given; a remote/--require-auth bench needs a gtk_ token)"
                raise SystemExit(f"auth failed: HTTP {e.code}{hint}\n{body.decode('utf-8', 'replace')[:300]}")
            raise SystemExit(f"HTTP {e.code} {e.reason} for {req.full_url}\n{body.decode('utf-8', 'replace')[:300]}")
        except urllib.error.URLError as e:
            raise SystemExit(f"cannot reach bench at {self.endpoint}: {e.reason}")

    def rpc(self, method, body, tolerate=()):
        """POST a ConnectRPC JSON request, return the parsed response dict.
        Codes in `tolerate` yield {} instead of aborting."""
        url = f"{self.endpoint}/gantry.v1.{method}"
        req = urllib.request.Request(
            url, data=json.dumps(body).encode(),
            headers=self._headers({"content-type": "application/json"}), method="POST",
        )
        status, raw = self._do(req, tolerate=tolerate)
        if status in tolerate or not raw:
            return {}
        try:
            return json.loads(raw)
        except json.JSONDecodeError:
            return {}

    def put_model(self, name, data, content_type):
        url = f"{self.endpoint}/models/{name}"
        req = urllib.request.Request(
            url, data=data, headers=self._headers({"content-type": content_type}), method="PUT",
        )
        status, _ = self._do(req)
        return status

    def list_models(self):
        req = urllib.request.Request(f"{self.endpoint}/models/", headers=self._headers(), method="GET")
        status, raw = self._do(req)
        try:
            return json.loads(raw).get("files", [])
        except json.JSONDecodeError:
            return []


# ---------------------------------------------------------------------------
# meshes: download (cached) + verify against the manifest
# ---------------------------------------------------------------------------

def sha256(data):
    return hashlib.sha256(data).hexdigest()


def fetch_mesh(entry, raw_base, cache_dir):
    """Return verified mesh bytes: from cache if the sha matches, else download."""
    name, want = entry["name"], entry["sha256"]
    cached = os.path.join(cache_dir, name)
    if os.path.exists(cached):
        data = open(cached, "rb").read()
        if sha256(data) == want:
            return data, "cache"
        step(f"{name}: cache sha mismatch, re-downloading")
    url = f"{raw_base}/{entry['source']}"
    req = urllib.request.Request(url, headers={"user-agent": "gantry-so101-setup"})
    try:
        with urllib.request.urlopen(req, timeout=60) as resp:
            data = resp.read()
    except (urllib.error.URLError, urllib.error.HTTPError) as e:
        raise SystemExit(f"download {name} failed: {e}")
    got = sha256(data)
    if got != want:
        raise SystemExit(f"sha256 mismatch for {name}\n  want {want}\n  got  {got}")
    os.makedirs(cache_dir, exist_ok=True)
    with open(cached, "wb") as f:
        f.write(data)
    return data, "download"


# ---------------------------------------------------------------------------
# viz_config_json (3D bindings) — envelope per apps/web/src/hardware.ts + pose.ts
# ---------------------------------------------------------------------------

def default_angle():
    return {"channelKey": None, "unit": "deg", "sign": 1}


def default_offset():
    return {"channelKey": None, "manual": 0}


def build_viz_config(pos_channel):
    """{"v":1,"bindings":PoseBindings} with every movable joint bound to its
    telemetry channel. channelKey = "<packet><U+001F><pos_channel>"."""
    joints = {}
    for j in JOINTS:
        joints[j] = {
            "mode": "channel",
            "channelKey": f"{j}{SEP}{pos_channel}",
            # Telemetry arrives in degrees (lerobot bridge: calibrated deg; gripper
            # 0-100% which maps 0..100 -> 0..1.745 rad, ~the joint's open range).
            # resolveJoint() converts deg->rad for the revolute joint.
            "unit": "deg",
            # Best-effort: URDF axis sign may be opposite the servo's positive
            # direction. Flip per-joint in the Bindings panel if a joint runs
            # backwards.
            "sign": 1,
            "manual": 0,
        }
    bindings = {
        "pitch": default_angle(), "roll": default_angle(), "yaw": default_angle(),
        "x": default_offset(), "y": default_offset(), "z": default_offset(),
        "joints": joints,
        # PrimitiveDims defaults (unused for a URDF model, but kept so the console's
        # mergeBindings round-trips cleanly).
        "dims": {
            "chassisLen": 0.4, "chassisWidth": 0.28, "chassisHeight": 0.12,
            "wheelRadius": 0.09, "wheelWidth": 0.04, "trackWidth": 0.34,
        },
    }
    return json.dumps({"v": 1, "bindings": bindings})


# ---------------------------------------------------------------------------
# workspace layout_json — envelope per apps/web/src/workspace/layout.ts
# ---------------------------------------------------------------------------

_id_seq = [0]


def panel_id():
    _id_seq[0] += 1
    return f"p-so101-{_id_seq[0]:02d}"


def panel(ptype, x, y, w, h, config, title=None):
    p = {"id": panel_id(), "type": ptype, "grid": {"x": x, "y": y, "w": w, "h": h}, "config": config}
    if title:
        p["title"] = title
    return p


def chan(device, packet, channel):
    return {"deviceId": device, "packet": packet, "channel": channel}


def build_layout(pos_channel):
    panels = []
    # Paired leader/follower position charts (2 per row, 6x6).
    for i, j in enumerate(CHART_JOINTS):
        x = (i % 2) * 6
        y = (i // 2) * 6
        panels.append(panel(
            "timeseries", x, y, 6, 6,
            {"channels": [chan("so101-leader", j, pos_channel), chan("so101-follower", j, pos_channel)]},
            title=f"{j} — leader vs follower",
        ))
    # Two 3D scenes side by side under the charts (5x8).
    y3d = ((len(CHART_JOINTS) + 1) // 2) * 6
    panels.append(panel("scene3d", 0, y3d, 5, 8, {"deviceId": "so101-leader"}, title="Leader 3D"))
    panels.append(panel("scene3d", 5, y3d, 5, 8, {"deviceId": "so101-follower"}, title="Follower 3D"))
    # Per-arm temperature readouts (value panels bind one channel each).
    yt = y3d + 8
    panels.append(panel("value", 0, yt, 3, 3,
                        {"channel": chan("so101-leader", TEMP_JOINT, "temp_c")},
                        title=f"Leader temp ({TEMP_JOINT})"))
    panels.append(panel("value", 3, yt, 3, 3,
                        {"channel": chan("so101-follower", TEMP_JOINT, "temp_c")},
                        title=f"Follower temp ({TEMP_JOINT})"))
    return json.dumps({"v": 1, "panels": panels})


# ---------------------------------------------------------------------------
# main
# ---------------------------------------------------------------------------

def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--endpoint", default="http://localhost:4780", help="bench base URL")
    ap.add_argument("--token", default=None, help="gtk_ access token (remote / --require-auth bench)")
    ap.add_argument("--pos-channel", default="pos",
                    help="joint position channel name to bind (default: pos, emitted by both bridge backends)")
    ap.add_argument("--cache-dir", default=os.path.join(MODEL_DIR, ".cache"),
                    help="mesh download cache (default: model/.cache, gitignored)")
    ap.add_argument("--workspace-name", default=WORKSPACE_NAME)
    ap.add_argument("--dry-run", action="store_true", help="print the plan without changing the bench")
    args = ap.parse_args()

    if not os.path.exists(MANIFEST_PATH):
        raise SystemExit(f"manifest not found: {MANIFEST_PATH}")
    manifest = json.load(open(MANIFEST_PATH))
    raw_base = manifest["upstream"]["raw_base"]
    urdf_path = os.path.join(MODEL_DIR, manifest["urdf"]["file"])
    urdf_bytes = open(urdf_path, "rb").read()

    bench = Bench(args.endpoint, args.token, args.dry_run)
    mode = "DRY RUN — no changes" if args.dry_run else args.endpoint
    print(f"SO-101 bench setup  ({mode})")
    print(f"  upstream: {manifest['upstream']['repo']} @ {manifest['upstream']['commit'][:12]} ({manifest['upstream']['license']})")
    print(f"  binding joints to channel '{args.pos_channel}'\n")

    # 1. meshes ----------------------------------------------------------------
    print("[1/4] meshes")
    meshes = manifest["meshes"]
    blobs = []
    for entry in meshes:
        data, src = fetch_mesh(entry, raw_base, args.cache_dir)
        blobs.append((entry["name"], data))
        step(f"{entry['name']:<40} {len(data):>8} B  verified ({src})")
    ok(f"{len(meshes)} meshes verified, {sum(len(d) for _, d in blobs)} B total")

    # 2. upload models ---------------------------------------------------------
    print("\n[2/4] upload to /models/")
    uploads = [(name, data, "model/stl") for name, data in blobs]
    # URDF once per device name (meshes are shared; uploaded once).
    for dev, _, _ in DEVICES:
        uploads.append((f"{dev}.urdf", urdf_bytes, "application/xml"))
    if args.dry_run:
        for name, data, ct in uploads:
            step(f"PUT /models/{name}  ({len(data)} B, {ct})")
    else:
        for name, data, ct in uploads:
            code = bench.put_model(name, data, ct)
            step(f"PUT /models/{name}  -> {code}")
        listed = set(bench.list_models())
        missing = [n for n, _, _ in uploads if n not in listed]
        if missing:
            fail(f"not listed after upload: {missing}")
        else:
            ok(f"{len(uploads)} files present in /models/")

    # 3. hardware --------------------------------------------------------------
    print("\n[3/4] hardware (viz bindings)")
    viz = build_viz_config(args.pos_channel)
    for dev, display, desc in DEVICES:
        if args.dry_run:
            step(f"UpsertHardware {dev}  displayName={display!r}  ({len(JOINTS)} joints bound)")
            continue
        # Preserve unrelated existing config (notes, panel defaults) if present.
        existing = bench.rpc("HardwareService/GetHardware", {"deviceId": dev}, tolerate=(404,)).get("hardware") or {}
        hw = {
            "deviceId": dev,
            "displayName": display,
            "description": desc,
            "notes": existing.get("notes", ""),
            "vizConfigJson": viz,
            "panelDefaultsJson": existing.get("panelDefaultsJson", ""),
        }
        bench.rpc("HardwareService/UpsertHardware", {"hardware": hw})
        step(f"UpsertHardware {dev}  ({len(JOINTS)} joints bound to '{args.pos_channel}')")
    if not args.dry_run:
        ok("both arms configured")

    # 4. workspace -------------------------------------------------------------
    print("\n[4/4] workspace")
    layout = build_layout(args.pos_channel)
    if args.dry_run:
        n = len(json.loads(layout)["panels"])
        step(f"UpsertWorkspace {args.workspace_name!r}  ({n} panels) — if absent")
    else:
        existing = bench.rpc("WorkspaceService/ListWorkspaces", {}).get("workspaces", []) or []
        names = {w.get("name") for w in existing}
        if args.workspace_name in names:
            ok(f"workspace {args.workspace_name!r} already exists — left untouched")
        else:
            bench.rpc("WorkspaceService/UpsertWorkspace",
                     {"workspace": {"name": args.workspace_name, "layoutJson": layout}})
            n = len(json.loads(layout)["panels"])
            ok(f"created workspace {args.workspace_name!r} ({n} panels)")

    print("\nDone." if not args.dry_run else "\nDry run complete.")
    if not args.dry_run:
        print("Open the bench, pick the SO-101 Teleop workspace, and start a bridge:")
        print("  python so101_lerobot_bridge.py --leader-port COMx --follower-port COMy")


if __name__ == "__main__":
    main()
