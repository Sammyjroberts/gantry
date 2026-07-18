//! Calibration: turn raw servo counts into the *same* normalized values lerobot publishes.
//!
//! lerobot normalizes `Present_Position` / `Goal_Position` via a per-motor calibration JSON keyed
//! by robot/teleop id, plus a per-motor *norm mode* from the robot config. Verified against
//! `huggingface/lerobot` (`main`):
//!
//! * **File** — `src/lerobot/robots/robot.py`: `calibration_fpath = calibration_dir / f"{id}.json"`,
//!   default `calibration_dir = HF_LEROBOT_CALIBRATION / "robots" / <robot_name>`
//!   (`~/.cache/huggingface/lerobot/calibration/robots/so_follower/<id>.json`; leaders live under
//!   `.../teleoperators/so_leader/<id>.json`).
//! * **Schema** — `motors/motors_bus.py::MotorCalibration`: a JSON object keyed by motor name, each
//!   `{ "id", "drive_mode", "homing_offset", "range_min", "range_max" }` (all integers).
//! * **Norm mode** — `robots/so_follower/so_follower.py`: body joints are `DEGREES` when
//!   `use_degrees` (config default **True**) else `RANGE_M100_100`; the gripper is always
//!   `RANGE_0_100`.
//! * **Normalize math** — `motors_bus.py::_normalize`, applied to the sign-magnitude-decoded value:
//!   - `DEGREES`: `(val - mid) * 360 / max_res`, `mid = (min+max)/2`, `max_res = 4096 - 1`.
//!   - `RANGE_0_100`: `((clamp(val,min,max) - min) / (max-min)) * 100`, then `100 - n` if drive_mode.
//!   - `RANGE_M100_100`: `((clamp - min)/(max-min))*200 - 100`, then `-n` if drive_mode.
//!
//! `homing_offset` is **not** applied here on purpose: Feetech servos report
//! `Present_Position = Actual_Position - Homing_Offset` in hardware (feetech.py), so the raw wire
//! value already carries it — re-applying it would double-count.

use std::collections::HashMap;
use std::path::Path;

use serde::Deserialize;

use crate::decoder::decode_sign_magnitude_15;

/// sts3215 model resolution (`MODEL_RESOLUTION["sts3215"]` in lerobot tables.py).
const MODEL_RESOLUTION: f64 = 4096.0;

/// The six SO-101 joints, indexed by Feetech servo id (`id → name`, lerobot convention).
pub const JOINTS: [(u8, &str); 6] = [
    (1, "shoulder_pan"),
    (2, "shoulder_lift"),
    (3, "elbow_flex"),
    (4, "wrist_flex"),
    (5, "wrist_roll"),
    (6, "gripper"),
];

/// Map a servo id to its joint name.
pub fn joint_name(servo_id: u8) -> Option<&'static str> {
    JOINTS
        .iter()
        .find(|(id, _)| *id == servo_id)
        .map(|(_, n)| *n)
}

/// Per-motor normalization mode (from the lerobot robot config, not the calibration file).
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum NormMode {
    /// Body joints when `use_degrees` (lerobot default): centered degrees.
    Degrees,
    /// The gripper: 0..100 %.
    Range0_100,
    /// Body joints when `use_degrees == false`: -100..100.
    RangeM100_100,
}

impl NormMode {
    /// Unit string published for a channel in this mode.
    pub fn unit(self) -> &'static str {
        match self {
            NormMode::Degrees => "deg",
            NormMode::Range0_100 | NormMode::RangeM100_100 => "%",
        }
    }
}

/// One motor's calibration record — mirrors lerobot's `MotorCalibration` dataclass exactly.
#[derive(Debug, Clone, Copy, Deserialize)]
pub struct MotorCalibration {
    pub id: i32,
    pub drive_mode: i32,
    pub homing_offset: i32,
    pub range_min: i32,
    pub range_max: i32,
}

/// A joint's calibration plus its norm mode.
#[derive(Debug, Clone, Copy)]
pub struct JointCalib {
    cal: MotorCalibration,
    norm: NormMode,
}

/// How the splitter turns raw counts into published values for one device.
pub enum Normalizer {
    /// Calibrated, matching lerobot output (degrees for body joints, 0..100 for the gripper).
    Calibrated {
        by_joint: HashMap<String, JointCalib>,
    },
    /// No calibration: raw-center degrees `((raw & 0xFFF) - 2048) * 360 / 4096`, uncalibrated.
    /// Mirrors the `so101_bridge.py` raw backend so `--raw` output is at least self-consistent.
    Raw,
}

impl Normalizer {
    /// Load a lerobot calibration JSON from `<dir>/<id>.json` and build a calibrated normalizer.
    ///
    /// `use_degrees` selects the body-joint mode (true → degrees, matching the lerobot config
    /// default; false → -100..100). The gripper is always 0..100.
    pub fn load(dir: &Path, id: &str, use_degrees: bool) -> Result<Self, String> {
        let path = dir.join(format!("{id}.json"));
        let text = std::fs::read_to_string(&path)
            .map_err(|e| format!("reading calibration {}: {e}", path.display()))?;
        let motors: HashMap<String, MotorCalibration> = serde_json::from_str(&text)
            .map_err(|e| format!("parsing calibration {}: {e}", path.display()))?;

        let body = if use_degrees {
            NormMode::Degrees
        } else {
            NormMode::RangeM100_100
        };
        let by_joint = motors
            .into_iter()
            .map(|(name, cal)| {
                let norm = if name == "gripper" {
                    NormMode::Range0_100
                } else {
                    body
                };
                (name, JointCalib { cal, norm })
            })
            .collect();
        Ok(Normalizer::Calibrated { by_joint })
    }

    /// Normalize a raw wire value for `joint`, returning `(value, unit)`. `None` when a calibrated
    /// normalizer has no record for that joint (published nowhere rather than published wrong).
    pub fn normalize(&self, joint: &str, raw: u16) -> Option<(f64, &'static str)> {
        let decoded = decode_sign_magnitude_15(raw);
        match self {
            Normalizer::Raw => {
                // Match so101_bridge.py's raw backend: mask to 12 bits, center at 2048.
                let masked = (raw & 0x0FFF) as f64;
                Some(((masked - 2048.0) * 360.0 / MODEL_RESOLUTION, "deg"))
            }
            Normalizer::Calibrated { by_joint } => {
                let jc = by_joint.get(joint)?;
                Some((normalize_value(decoded, jc), jc.norm.unit()))
            }
        }
    }
}

/// lerobot `MotorsBus._normalize` for a single value.
fn normalize_value(val: i32, jc: &JointCalib) -> f64 {
    let min = jc.cal.range_min as f64;
    let max = jc.cal.range_max as f64;
    let val = val as f64;
    let drive = jc.cal.drive_mode != 0;
    match jc.norm {
        NormMode::Degrees => {
            let mid = (min + max) / 2.0;
            let max_res = MODEL_RESOLUTION - 1.0;
            (val - mid) * 360.0 / max_res
        }
        NormMode::Range0_100 => {
            let bounded = val.clamp(min, max);
            let norm = if max != min {
                (bounded - min) / (max - min) * 100.0
            } else {
                0.0
            };
            if drive {
                100.0 - norm
            } else {
                norm
            }
        }
        NormMode::RangeM100_100 => {
            let bounded = val.clamp(min, max);
            let norm = if max != min {
                (bounded - min) / (max - min) * 200.0 - 100.0
            } else {
                0.0
            };
            if drive {
                -norm
            } else {
                norm
            }
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::io::Write;

    fn write_fixture(dir: &Path, id: &str, json: &str) {
        let mut f = std::fs::File::create(dir.join(format!("{id}.json"))).unwrap();
        f.write_all(json.as_bytes()).unwrap();
    }

    const FIXTURE: &str = r#"{
      "shoulder_pan":  {"id": 1, "drive_mode": 0, "homing_offset": -12, "range_min": 1000, "range_max": 3000},
      "shoulder_lift": {"id": 2, "drive_mode": 0, "homing_offset": 0,   "range_min": 900,  "range_max": 3100},
      "elbow_flex":    {"id": 3, "drive_mode": 0, "homing_offset": 0,   "range_min": 1024, "range_max": 3072},
      "wrist_flex":    {"id": 4, "drive_mode": 0, "homing_offset": 0,   "range_min": 1024, "range_max": 3072},
      "wrist_roll":    {"id": 5, "drive_mode": 0, "homing_offset": 0,   "range_min": 0,    "range_max": 4095},
      "gripper":       {"id": 6, "drive_mode": 0, "homing_offset": 0,   "range_min": 2000, "range_max": 3000}
    }"#;

    #[test]
    fn loads_schema_and_norm_modes() {
        let dir = tempfile::tempdir().unwrap();
        write_fixture(dir.path(), "my_arm", FIXTURE);
        let n = Normalizer::load(dir.path(), "my_arm", true).unwrap();

        // Body joint in degrees: at the exact midpoint the value is 0°.
        let (v, unit) = n.normalize("elbow_flex", 2048).unwrap();
        assert!(v.abs() < 1e-9, "midpoint should be 0deg, got {v}");
        assert_eq!(unit, "deg");

        // A quarter turn up from center of a full 0..4095 range ≈ 90°.
        let (v, _) = n.normalize("wrist_roll", 3072).unwrap(); // mid=2047.5, max_res=4095
        assert!((v - 90.0).abs() < 0.2, "expected ~90deg, got {v}");
    }

    #[test]
    fn gripper_is_percent() {
        let dir = tempfile::tempdir().unwrap();
        write_fixture(dir.path(), "arm", FIXTURE);
        let n = Normalizer::load(dir.path(), "arm", true).unwrap();
        // gripper range 2000..3000: min→0%, max→100%, mid→50%.
        assert_eq!(n.normalize("gripper", 2000).unwrap(), (0.0, "%"));
        assert_eq!(n.normalize("gripper", 3000).unwrap(), (100.0, "%"));
        let (v, unit) = n.normalize("gripper", 2500).unwrap();
        assert!((v - 50.0).abs() < 1e-9);
        assert_eq!(unit, "%");
        // Out-of-range clamps.
        assert_eq!(n.normalize("gripper", 1500).unwrap().0, 0.0);
        assert_eq!(n.normalize("gripper", 3500).unwrap().0, 100.0);
    }

    #[test]
    fn no_degrees_gives_m100_100_for_body() {
        let dir = tempfile::tempdir().unwrap();
        write_fixture(dir.path(), "arm", FIXTURE);
        let n = Normalizer::load(dir.path(), "arm", false).unwrap();
        // wrist_roll 0..4095: min→-100, max→+100, mid→0.
        assert_eq!(n.normalize("wrist_roll", 0).unwrap(), (-100.0, "%"));
        assert_eq!(n.normalize("wrist_roll", 4095).unwrap(), (100.0, "%"));
    }

    #[test]
    fn drive_mode_flips_gripper() {
        let dir = tempfile::tempdir().unwrap();
        write_fixture(
            dir.path(),
            "arm",
            r#"{"gripper": {"id": 6, "drive_mode": 1, "homing_offset": 0, "range_min": 2000, "range_max": 3000}}"#,
        );
        let n = Normalizer::load(dir.path(), "arm", true).unwrap();
        // drive_mode=1 flips: min→100, max→0.
        assert_eq!(n.normalize("gripper", 2000).unwrap().0, 100.0);
        assert_eq!(n.normalize("gripper", 3000).unwrap().0, 0.0);
    }

    #[test]
    fn raw_normalizer_matches_python() {
        let n = Normalizer::Raw;
        // (2048 & 0xFFF - 2048) * 360/4096 == 0.
        assert_eq!(n.normalize("anything", 2048).unwrap(), (0.0, "deg"));
        let (v, _) = n.normalize("x", 3072).unwrap(); // (3072-2048)*360/4096 = 90
        assert!((v - 90.0).abs() < 1e-9);
    }

    #[test]
    fn missing_joint_yields_none() {
        let dir = tempfile::tempdir().unwrap();
        write_fixture(dir.path(), "arm", FIXTURE);
        let n = Normalizer::load(dir.path(), "arm", true).unwrap();
        assert!(n.normalize("nonexistent_joint", 2048).is_none());
    }
}
