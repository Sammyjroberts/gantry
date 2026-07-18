# gantry-foxglove

A generic **Foxglove-WebSocket → Gantry** tap. It subscribes to any server that
speaks the open [`foxglove.websocket.v1`](https://github.com/foxglove/ws-protocol)
protocol, maps advertised channels to Gantry devices/packets/channels with a
config-driven rule set, and publishes over Gantry's plain-HTTP JSON ingest.
Because it targets the protocol (not any one producer) it works with:

- **lerobot** — `lerobot-teleoperate --display_mode=foxglove` starts a
  foxglove-sdk server; a built-in profile maps SO-101 teleop straight into a
  Bench.
- **ROS 2** — point it at a running `foxglove_bridge` and write a small mapping
  file for your scalar topics.
- Anything else that advertises scalar (JSON) channels over the protocol.

It is the first resident of the monorepo's top-level `adapters/` directory.

## Install

```
pip install websockets      # required
pip install av              # optional — enables the image → video tee
```

(`av` = PyAV. Without it the adapter streams scalars only and logs a one-time
note that the video tee is disabled.)

## lerobot quickstart

Three processes, in any order — the tap reconnects until the Foxglove server is
up, and the Bench can boot after the tap (channels/units are (re)registered on
connect):

1. **Bench** — your Gantry Bench listening on `http://localhost:4780`.
2. **lerobot teleop with the Foxglove backend** (starts a WS server on
   `127.0.0.1:8765`):

   ```
   lerobot-teleoperate \
       --robot.type=so101_follower --robot.port=/dev/ttyACM0 \
       --teleop.type=so101_leader  --teleop.port=/dev/ttyACM1 \
       --display_mode=foxglove
   ```

3. **This tap**:

   ```
   cd adapters/foxglove
   python -m gantry_foxglove --endpoint http://localhost:4780 --url ws://127.0.0.1:8765
   # remote bench:  --endpoint https://bench.example --token gtk_...
   ```

The Foxglove desktop app can subscribe to `ws://127.0.0.1:8765` at the same time
— the server fans out to multiple subscribers, so you get live plots in Gantry
*and* the Foxglove app off one teleop session.

### What the lerobot profile produces

Verified against lerobot's foxglove backend
(`src/lerobot/utils/foxglove_visualization.py`): scalars ride a static
`lerobot.Scalars` JSON schema — a `scalars` array of `{label, value}` — on topic
`/observation/state` (observations) and `/action/state` (actions). For an SO-101
the labels are the robot keys `"<joint>.pos"`.

| Foxglove topic        | label          | → Gantry device   | packet     | channel     | unit |
|-----------------------|----------------|-------------------|------------|-------------|------|
| `/observation/state`  | `<joint>.pos`  | `so101-follower`  | `<joint>`  | `pos`       | deg  |
| `/action/state`       | `<joint>.pos`  | `so101-leader`    | `<joint>`  | `pos`       | deg  |
| `/action/state`       | `<joint>.pos`  | `so101-follower`  | `<joint>`  | `cmd`       | deg  |
| *(derived)*           |                | `so101-follower`  | `<joint>`  | `track_err` | deg  |

An action is **both** the leader's pose and the follower's command, so it is
published to both devices. `track_err = cmd − pos` per joint on the follower is
the teleop-quality signal, computed by the tap (the same convention as the
SO-101 kit's `so101_bridge.py`). Frame timestamps are the message's Foxglove
`log_time` (nanoseconds) — packet time straight from the producer's clock.

## Mapping config format (`--map map.json`)

The built-in profiles and `--map` share one JSON shape. A mapping is a `name`, a
list of `rules`, and an optional `track_err` block.

```jsonc
{
  "name": "my-robot",
  "rules": [
    {
      "topic": "/observation/state",     // exact match ...
      // "topic_prefix": "/sensors/",    // ... or a prefix match
      "kind": "scalars",                 // "scalars" | "image"
      "scalars_field": "scalars",        // JSON key holding the {label,value} list
      "strip_suffix": ".pos",            // trim label before use (also strip_prefix)
      "emit": [
        // Each label fans out to one or more targets. Fix ONE of packet/channel
        // and take the OTHER from the label:
        {"device": "so101-follower", "channel": "pos",
         "packet_from_label": true, "unit": "deg", "scale": 1.0}
        // ...or label -> channel:
        // {"device": "imu0", "packet": "imu", "channel_from_label": true, "unit": "m/s^2"}
      ]
    },
    {
      "topic_prefix": "/observation/images/",
      "kind": "image",
      "camera_from_topic": true          // camera id = last topic segment, sanitised
      // "camera": "front"               // ...or a fixed camera id
    }
  ],
  "track_err": {                          // optional derived error channel
    "device": "so101-follower",
    "pos_channel": "pos",
    "cmd_channel": "cmd",
    "out_channel": "track_err",
    "unit": "deg"
  }
}
```

Rule fields:

- **`topic`** / **`topic_prefix`** — how the rule matches an advertised topic.
- **`kind`** — `scalars` (map the `{label,value}` list) or `image` (tee to video).
- **scalars**: `scalars_field` (default `"scalars"`), `strip_prefix` /
  `strip_suffix` (label cleanup), and `emit[]`. Each emit names a `device`, fixes
  one of `packet`/`channel` and sets the other via `packet_from_label` /
  `channel_from_label`, plus optional `unit` and `scale`.
- **image**: `camera_from_topic` (camera id = last path segment, coerced to
  `[A-Za-z0-9_-]+`) or an explicit `camera`.
- **`track_err`**: derives `out_channel = cmd_channel − pos_channel` per packet on
  one device, emitted once both inputs are seen.

Channels are registered lazily with their units the first time a
(packet, channel) pair appears — joints/topics are discovered from the stream.

## Video tee

If PyAV is installed, `image` topics carrying foxglove `CompressedImage` JPEG
frames are decoded and re-encoded into rolling ~2 s H.264 MP4 chunks and POSTed
to Gantry's video lane (`POST /video/chunks`), with the camera id taken from the
topic's last segment. This runs on a dedicated worker thread so encoding never
stalls the WebSocket loop; frames that back up are dropped rather than buffered
unbounded. Tune with `--video-fps` / `--video-seconds`, or disable with
`--no-video`.

For lerobot you get JPEG frames when teleop logs compressed images
(`compress_images=True`); raw (uncompressed) image frames are not teed. If PyAV
is absent the adapter continues scalars-only.

## Tests

```
pip install websockets av numpy pytest
python -m pytest adapters/foxglove -q
```

The suite needs no network beyond localhost and no lerobot/foxglove-sdk: a fake
in-process server speaks the protocol subset (advertise, subscribe, binary
MessageData, unadvertise, connection drop) and the tap is asserted end-to-end
against a mocked publisher. The video test is skipped cleanly when PyAV/numpy are
unavailable.
