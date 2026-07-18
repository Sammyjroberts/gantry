"""Mapping / lerobot-profile tests (pure, no network)."""

from gantry_foxglove.mapping import Mapping, lerobot_profile, sanitize_camera


def _by(frames):
    """Index frames as {(device, packet, channel): (value, t_ns)}."""
    return {(d, p, c): (v, t) for d, p, c, v, t in frames}


def test_observation_maps_to_follower_pos():
    m = lerobot_profile()
    rule = m.match("/observation/state")
    assert rule is not None and rule.kind == "scalars"
    frames = m.map_scalars(rule, [{"label": "shoulder_pan.pos", "value": 10.0},
                                  {"label": "gripper.pos", "value": 5.0}], 111)
    idx = _by(frames)
    assert idx[("so101-follower", "shoulder_pan", "pos")] == (10.0, 111)
    assert idx[("so101-follower", "gripper", "pos")] == (5.0, 111)


def test_action_maps_to_leader_pos_and_follower_cmd():
    m = lerobot_profile()
    rule = m.match("/action/state")
    frames = m.map_scalars(rule, [{"label": "elbow_flex.pos", "value": 20.0}], 222)
    idx = _by(frames)
    assert idx[("so101-leader", "elbow_flex", "pos")] == (20.0, 222)
    assert idx[("so101-follower", "elbow_flex", "cmd")] == (20.0, 222)


def test_track_err_emitted_after_pos_and_cmd():
    m = lerobot_profile()
    # Observation first (pos=10), then action (cmd=12): track_err = cmd - pos = 2.
    m.map_scalars(m.match("/observation/state"), [{"label": "wrist_roll.pos", "value": 10.0}], 100)
    frames = m.map_scalars(m.match("/action/state"), [{"label": "wrist_roll.pos", "value": 12.0}], 200)
    idx = _by(frames)
    assert idx[("so101-follower", "wrist_roll", "track_err")] == (2.0, 200)


def test_no_track_err_before_both_known():
    m = lerobot_profile()
    frames = m.map_scalars(m.match("/observation/state"), [{"label": "gripper.pos", "value": 1.0}], 5)
    assert all(c != "track_err" for _, _, c, _, _ in frames)


def test_image_rule_and_camera_id():
    m = lerobot_profile()
    rule = m.match("/observation/images/front")
    assert rule is not None and rule.kind == "image"
    assert m.camera_for(rule, "/observation/images/front") == "front"
    assert m.wants_images()


def test_sanitize_camera():
    assert sanitize_camera("front") == "front"
    assert sanitize_camera("wrist.cam") == "wrist_cam"
    assert sanitize_camera("a/b") == "a_b"
    assert sanitize_camera("") == "cam"


def test_channel_from_label_generic_rule():
    # A generic ROS-style rule: fixed packet, label -> channel.
    m = Mapping.from_dict({
        "name": "custom",
        "rules": [{
            "topic": "/imu",
            "kind": "scalars",
            "emit": [{"device": "imu0", "packet": "imu", "channel_from_label": True, "unit": "m/s^2"}],
        }],
    })
    rule = m.match("/imu")
    frames = m.map_scalars(rule, [{"label": "ax", "value": 9.8}], 7)
    assert _by(frames)[("imu0", "imu", "ax")] == (9.8, 7)


def test_scale_and_strip_prefix():
    m = Mapping.from_dict({
        "name": "custom",
        "rules": [{
            "topic": "/t",
            "kind": "scalars",
            "strip_prefix": "j_",
            "emit": [{"device": "d", "channel": "pos", "packet_from_label": True, "scale": 2.0}],
        }],
    })
    frames = m.map_scalars(m.match("/t"), [{"label": "j_a", "value": 3.0}], 1)
    assert _by(frames)[("d", "a", "pos")] == (6.0, 1)
