use gantry_tlm as tlm;

// Telemetry requires named fields.
#[derive(tlm::Telemetry)]
struct Bad(f32, f32);

fn main() {}
