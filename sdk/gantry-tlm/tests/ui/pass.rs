use gantry_tlm as tlm;

#[derive(tlm::Telemetry)]
#[tlm(packet = "imu")]
struct Imu {
    #[tlm(unit = "deg")]
    pitch: f32,
    #[tlm(name = "gy", unit = "dps")]
    gyro_y: i16,
    ok: bool,
}

fn main() {
    let _ = <Imu as tlm::Telemetry>::PACKET;
    let _ = <Imu as tlm::Telemetry>::FIELDS;
}
