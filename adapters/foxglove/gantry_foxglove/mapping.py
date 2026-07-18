"""Config-driven mapping from Foxglove topics to Gantry device/packet/channel.

A mapping is a set of *rules*, each matching a topic (exact or by prefix) and
saying how that topic's data becomes Gantry frames:

* **scalar** rules cover the flat ``{label, value}`` list that lerobot (and the
  generic pattern) put on a topic. Each rule fans every label out to one or more
  emit targets. A target names a device and fixes one of packet/channel while
  taking the other *from the label* — so ``label -> packet`` (channel fixed) and
  ``label -> channel`` (packet fixed) are both expressible. Labels can be
  stripped of a suffix/prefix first (e.g. ``shoulder_pan.pos`` -> packet
  ``shoulder_pan``), and values scaled.

* **image** rules mark a topic (usually a prefix) whose messages are camera
  frames to hand to the optional video tee; the camera id is the topic's last
  path segment (sanitised) or an explicit value.

An optional **track_err** block derives a per-packet error channel from two
already-mapped channels on one device (e.g. follower ``cmd`` minus ``pos``).

Timestamps are always the frame's ``log_time`` (ns) — it is packet time from the
producer's clock, so nothing here invents a timestamp.

The ``--map map.json`` file and the built-in ``lerobot`` profile share this exact
shape; see :func:`Mapping.from_dict` for the JSON schema and
:func:`lerobot_profile` for a worked example.
"""

from __future__ import annotations

import re
from dataclasses import dataclass, field

# A produced frame, ready for a GantryPublisher: device, packet, channel, value, ns.
Frame = tuple[str, str, str, float, int]


def sanitize_camera(name: str) -> str:
    """Coerce a topic segment into a valid Gantry camera id ([A-Za-z0-9_-]+).

    Mirrors the Bench's validCameraID rule (dots and slashes are disallowed):
    every other character becomes ``_``. Empty input becomes ``"cam"``.
    """
    out = re.sub(r"[^A-Za-z0-9_-]", "_", name).strip("_")
    return out or "cam"


@dataclass
class Emit:
    """One publish target for a scalar label."""

    device: str
    # Exactly one of channel / channel_from_label, and one of packet /
    # packet_from_label, is active.
    channel: str | None = None
    channel_from_label: bool = False
    packet: str | None = None
    packet_from_label: bool = False
    unit: str | None = None
    scale: float = 1.0

    def resolve(self, label: str) -> tuple[str, str]:
        """Return (packet, channel) for a given (already-stripped) label."""
        packet = label if self.packet_from_label else (self.packet or "")
        channel = label if self.channel_from_label else (self.channel or "")
        return packet, channel

    @classmethod
    def from_dict(cls, obj: dict) -> "Emit":
        return cls(
            device=obj["device"],
            channel=obj.get("channel"),
            channel_from_label=bool(obj.get("channel_from_label", False)),
            packet=obj.get("packet"),
            packet_from_label=bool(obj.get("packet_from_label", False)),
            unit=obj.get("unit"),
            scale=float(obj.get("scale", 1.0)),
        )


@dataclass
class Rule:
    """A topic-matching rule."""

    kind: str  # "scalars" | "image"
    topic: str | None = None
    topic_prefix: str | None = None
    # scalars
    scalars_field: str = "scalars"
    strip_suffix: str | None = None
    strip_prefix: str | None = None
    emit: list[Emit] = field(default_factory=list)
    # image
    camera: str | None = None
    camera_from_topic: bool = True

    def matches(self, topic: str) -> bool:
        if self.topic is not None:
            return topic == self.topic
        if self.topic_prefix is not None:
            return topic.startswith(self.topic_prefix)
        return False

    def strip(self, label: str) -> str:
        if self.strip_prefix and label.startswith(self.strip_prefix):
            label = label[len(self.strip_prefix) :]
        if self.strip_suffix and label.endswith(self.strip_suffix):
            label = label[: -len(self.strip_suffix)]
        return label

    @classmethod
    def from_dict(cls, obj: dict) -> "Rule":
        return cls(
            kind=obj.get("kind", "scalars"),
            topic=obj.get("topic"),
            topic_prefix=obj.get("topic_prefix"),
            scalars_field=obj.get("scalars_field", "scalars"),
            strip_suffix=obj.get("strip_suffix"),
            strip_prefix=obj.get("strip_prefix"),
            emit=[Emit.from_dict(e) for e in obj.get("emit", [])],
            camera=obj.get("camera"),
            camera_from_topic=bool(obj.get("camera_from_topic", True)),
        )


@dataclass
class TrackErr:
    """Derive ``out_channel = cmd_channel - pos_channel`` per packet on device.

    Matches the SO-101 kit's tracking-error convention (leader/command pose minus
    follower/observed pose). Emitted on whichever of the two inputs arrives once
    both are known for a packet, stamped at that input's log_time.
    """

    device: str
    pos_channel: str
    cmd_channel: str
    out_channel: str = "track_err"
    unit: str | None = None

    @classmethod
    def from_dict(cls, obj: dict) -> "TrackErr":
        return cls(
            device=obj["device"],
            pos_channel=obj["pos_channel"],
            cmd_channel=obj["cmd_channel"],
            out_channel=obj.get("out_channel", "track_err"),
            unit=obj.get("unit"),
        )


class Mapping:
    """A named set of rules plus optional derived track_err. Stateful only for
    track_err (it caches the latest pos/cmd per packet)."""

    def __init__(self, name: str, rules: list[Rule], track_err: TrackErr | None = None):
        self.name = name
        self.rules = rules
        self.track_err = track_err
        # (packet) -> {"pos": value, "cmd": value} latest, for track_err.
        self._te_cache: dict[str, dict[str, float]] = {}

    @classmethod
    def from_dict(cls, obj: dict) -> "Mapping":
        """Build a Mapping from a plain dict / parsed JSON:

        ``{"name": str, "rules": [Rule...], "track_err": TrackErr?}``
        """
        te = obj.get("track_err")
        return cls(
            name=obj.get("name", "custom"),
            rules=[Rule.from_dict(r) for r in obj.get("rules", [])],
            track_err=TrackErr.from_dict(te) if te else None,
        )

    def match(self, topic: str) -> Rule | None:
        """First rule matching ``topic``, or None."""
        for rule in self.rules:
            if rule.matches(topic):
                return rule
        return None

    def wants_images(self) -> bool:
        return any(r.kind == "image" for r in self.rules)

    def camera_for(self, rule: Rule, topic: str) -> str:
        if rule.camera:
            return sanitize_camera(rule.camera)
        # Last path segment of the topic.
        seg = topic.rstrip("/").rsplit("/", 1)[-1]
        return sanitize_camera(seg)

    def map_scalars(self, rule: Rule, scalars: list[dict], log_time: int) -> list[Frame]:
        """Turn a scalar message (list of ``{label, value}``) into frames,
        including any derived track_err."""
        frames: list[Frame] = []
        for item in scalars:
            try:
                label = str(item["label"])
                value = float(item["value"])
            except (KeyError, TypeError, ValueError):
                continue
            label = rule.strip(label)
            for em in rule.emit:
                packet, channel = em.resolve(label)
                if not packet or not channel:
                    continue
                val = value * em.scale
                frames.append((em.device, packet, channel, val, log_time))
                frames.extend(self._track_err(em.device, packet, channel, val, log_time))
        return frames

    def _track_err(self, device: str, packet: str, channel: str, value: float, log_time: int) -> list[Frame]:
        te = self.track_err
        if te is None or device != te.device:
            return []
        if channel == te.pos_channel:
            slot = "pos"
        elif channel == te.cmd_channel:
            slot = "cmd"
        else:
            return []
        cache = self._te_cache.setdefault(packet, {})
        cache[slot] = value
        if "pos" in cache and "cmd" in cache:
            err = cache["cmd"] - cache["pos"]
            return [(te.device, packet, te.out_channel, err, log_time)]
        return []


# --- built-in lerobot profile ---------------------------------------------
#
# Verified against lerobot's foxglove backend
# (src/lerobot/utils/foxglove_visualization.py, constants.py):
#   * Scalars ride on a static JSON schema "lerobot.Scalars": a "scalars" array
#     of {label, value}. Observations go on topic "/observation/state", actions
#     on "/action/state" (_foxglove_topic -> "/{source}/state").
#   * For an SO-101 the labels are the robot dict keys, i.e. "<joint>.pos"
#     (shoulder_pan.pos, ..., gripper.pos) — no prefix, dot preserved (only the
#     topic path is dot-sanitised, not the scalar labels).
#   * Camera frames go on "/observation/images/<name>" (dots -> underscores),
#     as foxglove CompressedImage (JPEG) when compress_images is set.
#
# Mapping decisions (per the adapter spec):
#   observation "<joint>.pos" -> so101-follower / packet <joint> / channel pos
#   action      "<joint>.pos" -> so101-leader   / packet <joint> / channel pos
#                             AND so101-follower / packet <joint> / channel cmd
#                                 (an action is both the leader's pose and the
#                                  follower's command — published to both)
#   track_err (follower) = cmd - pos per joint.

_LEROBOT_PROFILE = {
    "name": "lerobot",
    "rules": [
        {
            "topic": "/observation/state",
            "kind": "scalars",
            "strip_suffix": ".pos",
            "emit": [
                {"device": "so101-follower", "channel": "pos", "packet_from_label": True, "unit": "deg"}
            ],
        },
        {
            "topic": "/action/state",
            "kind": "scalars",
            "strip_suffix": ".pos",
            "emit": [
                {"device": "so101-leader", "channel": "pos", "packet_from_label": True, "unit": "deg"},
                {"device": "so101-follower", "channel": "cmd", "packet_from_label": True, "unit": "deg"},
            ],
        },
        {
            "topic_prefix": "/observation/images/",
            "kind": "image",
            "camera_from_topic": True,
        },
    ],
    "track_err": {
        "device": "so101-follower",
        "pos_channel": "pos",
        "cmd_channel": "cmd",
        "out_channel": "track_err",
        "unit": "deg",
    },
}


def lerobot_profile() -> Mapping:
    """The built-in lerobot mapping (SO-101 leader/follower teleop)."""
    return Mapping.from_dict(_LEROBOT_PROFILE)


def load_profile(name: str) -> Mapping:
    if name == "lerobot":
        return lerobot_profile()
    raise ValueError(f"unknown built-in profile {name!r} (known: lerobot)")
