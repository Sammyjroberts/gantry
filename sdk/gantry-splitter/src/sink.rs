//! Wiring decoder [`Reading`]s to a [`Publisher`]: normalize, publish `pos`/`cmd`, and compute
//! `track_err` on the follower.
//!
//! This is the semantic layer that matches the existing SO-101 kit
//! (`so101_bridge.py` / `so101_teleop_gantry.py`): per-joint `pos`, `cmd` on the follower (the
//! teleop `Goal_Position` targets), and `track_err = leader_pos - follower_pos` published on the
//! follower device every time the follower reports a position, against the latest leader snapshot.

use std::collections::HashMap;
use std::sync::{Arc, Mutex};

use gantry_edge::{ChannelInfo, ValueKind};

use crate::calibration::{joint_name, Normalizer, JOINTS};
use crate::decoder::{Channel, Reading};
use crate::publish::{now_ns, Publisher};

/// Which arm a port represents.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Role {
    Leader,
    Follower,
}

/// The latest per-joint leader positions, shared between the two ports' sinks for `track_err`.
#[derive(Default)]
pub struct SharedLeader {
    pos: Mutex<HashMap<String, f64>>,
}

impl SharedLeader {
    /// A fresh, empty snapshot.
    pub fn new() -> Arc<Self> {
        Arc::new(Self::default())
    }
    fn update(&self, joint: &str, value: f64) {
        if let Ok(mut g) = self.pos.lock() {
            g.insert(joint.to_string(), value);
        }
    }
    fn get(&self, joint: &str) -> Option<f64> {
        self.pos.lock().ok().and_then(|g| g.get(joint).copied())
    }
}

/// Consumes readings for one port and publishes them. All state is interior-mutable so both pump
/// threads share one `Arc<PortSink>`.
pub struct PortSink {
    role: Role,
    publisher: Arc<Publisher>,
    normalizer: Normalizer,
    shared_leader: Arc<SharedLeader>,
    emit_track: bool,
}

impl PortSink {
    /// Build a sink. `emit_track` should be true only for the follower when both arms are present.
    pub fn new(
        role: Role,
        publisher: Arc<Publisher>,
        normalizer: Normalizer,
        shared_leader: Arc<SharedLeader>,
        emit_track: bool,
    ) -> Arc<Self> {
        Arc::new(Self {
            role,
            publisher,
            normalizer,
            shared_leader,
            emit_track,
        })
    }

    /// Publish a batch of decoded readings (called by the pump after forwarding the bytes).
    pub fn on_readings(&self, readings: &[Reading]) {
        if readings.is_empty() {
            return;
        }
        let t_ns = now_ns();
        for r in readings {
            let Some(joint) = joint_name(r.servo_id) else {
                continue;
            };
            let Some((value, _unit)) = self.normalizer.normalize(joint, r.raw) else {
                continue;
            };
            match r.channel {
                Channel::Pos => {
                    self.publisher.add(joint, "pos", value, t_ns);
                    if self.role == Role::Leader {
                        self.shared_leader.update(joint, value);
                    } else if self.emit_track {
                        if let Some(lead) = self.shared_leader.get(joint) {
                            self.publisher.add(joint, "track_err", lead - value, t_ns);
                        }
                    }
                }
                Channel::Cmd => {
                    self.publisher.add(joint, "cmd", value, t_ns);
                }
            }
        }
    }
}

/// Channel metadata for one device, with the correct **per-joint** unit taken from the normalizer
/// (body joints in `deg` or `%`, the gripper in `%`).
///
/// Registers, per joint: `pos` (always) and `cmd` (always — a `Goal_Position` write can appear on
/// either arm), plus `track_err` on the follower when both arms run.
pub fn channel_specs(normalizer: &Normalizer, emit_track: bool) -> Vec<ChannelInfo> {
    let mut ch = Vec::new();
    for (_, joint) in JOINTS {
        // Probe the joint's unit at its raw midpoint; fall back to deg if uncalibrated for it.
        let unit = normalizer
            .normalize(joint, 2048)
            .map(|(_, u)| u)
            .unwrap_or("deg");
        ch.push(info(joint, "pos", unit));
        ch.push(info(joint, "cmd", unit));
        if emit_track {
            ch.push(info(joint, "track_err", unit));
        }
    }
    ch
}

fn info(packet: &str, channel: &str, unit: &str) -> ChannelInfo {
    ChannelInfo {
        name: channel.to_string(),
        kind: ValueKind::F64 as i32,
        unit: unit.to_string(),
        description: String::new(),
        packet: packet.to_string(),
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::decoder::Channel;
    use gantry_edge::{FrameBatch, Transport, TransportError};

    #[derive(Clone, Default)]
    struct Mock {
        batches: Arc<Mutex<Vec<FrameBatch>>>,
    }
    impl Transport for Mock {
        fn publish(&self, batch: FrameBatch) -> Result<u64, TransportError> {
            let seq = batch.sequence;
            self.batches.lock().unwrap().push(batch);
            Ok(seq)
        }
    }

    fn frames(m: &Mock) -> Vec<gantry_edge::Frame> {
        m.batches
            .lock()
            .unwrap()
            .iter()
            .flat_map(|b| b.frames.clone())
            .collect()
    }

    #[test]
    fn track_err_published_on_follower() {
        let shared = SharedLeader::new();

        // Leader reports shoulder_pan pos in raw mode: raw 3072 → 90deg.
        let leader_pub = Publisher::start(Mock::default(), "so101-leader", vec![]);
        let leader = PortSink::new(
            Role::Leader,
            leader_pub,
            Normalizer::Raw,
            shared.clone(),
            false,
        );
        leader.on_readings(&[Reading {
            servo_id: 1,
            channel: Channel::Pos,
            raw: 3072,
        }]);

        // Follower reports the same joint at raw 2048 → 0deg. track_err = 90 - 0 = 90.
        let mock = Mock::default();
        let follower_pub = Publisher::start(mock.clone(), "so101-follower", vec![]);
        let follower = PortSink::new(
            Role::Follower,
            follower_pub.clone(),
            Normalizer::Raw,
            shared,
            true,
        );
        follower.on_readings(&[Reading {
            servo_id: 1,
            channel: Channel::Pos,
            raw: 2048,
        }]);
        follower_pub.shutdown();

        let fs = frames(&mock);
        let track: Vec<_> = fs.iter().filter(|f| f.channel == "track_err").collect();
        assert_eq!(track.len(), 1);
        assert_eq!(track[0].packet, "shoulder_pan");
        if let Some(gantry_edge::value::Kind::F64(v)) =
            track[0].value.as_ref().and_then(|v| v.kind.clone())
        {
            assert!((v - 90.0).abs() < 1e-9, "track_err should be 90, got {v}");
        } else {
            panic!("expected f64 track_err");
        }
    }

    #[test]
    fn cmd_channel_from_goal_write() {
        let mock = Mock::default();
        let pub_f = Publisher::start(mock.clone(), "so101-follower", vec![]);
        let sink = PortSink::new(
            Role::Follower,
            pub_f.clone(),
            Normalizer::Raw,
            SharedLeader::new(),
            true,
        );
        sink.on_readings(&[Reading {
            servo_id: 2,
            channel: Channel::Cmd,
            raw: 2048,
        }]);
        pub_f.shutdown();
        let fs = frames(&mock);
        assert!(fs
            .iter()
            .any(|f| f.channel == "cmd" && f.packet == "shoulder_lift"));
    }
}
