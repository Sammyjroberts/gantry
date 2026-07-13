use gantry_tlm as tlm;

// Unknown #[tlm(...)] key on a field.
#[derive(tlm::Telemetry)]
struct Bad {
    #[tlm(scale = "2")]
    x: f32,
}

fn main() {}
