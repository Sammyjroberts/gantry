use gantry_tlm as tlm;

// &str is not supported as a struct field in v1 (owned heapless strings are out of scope).
#[derive(tlm::Telemetry)]
struct Bad<'a> {
    label: &'a str,
}

fn main() {}
