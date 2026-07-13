//! `#[derive(Telemetry)]` expansion behavior. Run with `--features enabled`.

#![cfg(feature = "enabled")]

use gantry_tlm as tlm;
use tlm::{Kind, Telemetry, ValueWriter};

/// Records the visitor calls made by `write_values`.
#[derive(Default)]
struct Rec {
    calls: Vec<String>,
}

impl ValueWriter for Rec {
    fn field_f32(&mut self, v: f32) {
        self.calls.push(format!("f32:{v}"));
    }
    fn field_f64(&mut self, v: f64) {
        self.calls.push(format!("f64:{v}"));
    }
    fn field_i32(&mut self, v: i32) {
        self.calls.push(format!("i32:{v}"));
    }
    fn field_i64(&mut self, v: i64) {
        self.calls.push(format!("i64:{v}"));
    }
    fn field_bool(&mut self, v: bool) {
        self.calls.push(format!("bool:{v}"));
    }
    fn field_str(&mut self, v: &str) {
        self.calls.push(format!("str:{v}"));
    }
}

#[derive(Telemetry)]
struct ImuState {
    #[tlm(unit = "deg")]
    pitch: f32,
    #[tlm(name = "gy", unit = "dps")]
    gyro_y: f64,
    count: u16,
    flag: bool,
}

#[derive(Telemetry)]
#[tlm(packet = "power")]
struct Battery {
    volts: f32,
}

#[test]
fn packet_name_defaults_to_snake_case() {
    assert_eq!(<ImuState as Telemetry>::PACKET, "imu_state");
}

#[test]
fn packet_attribute_overrides_name() {
    assert_eq!(<Battery as Telemetry>::PACKET, "power");
}

#[test]
fn field_descriptors_carry_name_kind_unit() {
    let f = <ImuState as Telemetry>::FIELDS;
    assert_eq!(f.len(), 4);

    assert_eq!(f[0].name, "pitch");
    assert_eq!(f[0].unit, "deg");
    assert_eq!(f[0].kind, Kind::F32);

    // #[tlm(name = ...)] overrides the field name.
    assert_eq!(f[1].name, "gy");
    assert_eq!(f[1].unit, "dps");
    assert_eq!(f[1].kind, Kind::F64);

    // Integers (here u16) widen to i64 on the wire.
    assert_eq!(f[2].name, "count");
    assert_eq!(f[2].unit, "");
    assert_eq!(f[2].kind, Kind::I64);

    assert_eq!(f[3].kind, Kind::Bool);
}

#[test]
fn write_values_visits_fields_in_order_and_widens_ints() {
    let mut rec = Rec::default();
    ImuState {
        pitch: 1.0,
        gyro_y: 2.5,
        count: 3,
        flag: true,
    }
    .write_values(&mut rec);
    assert_eq!(rec.calls, ["f32:1", "f64:2.5", "i64:3", "bool:true"]);
}
