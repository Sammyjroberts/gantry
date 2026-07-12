//! Compile `proto/gantry/v1/*.proto` into prost types using protox (pure Rust),
//! so building this crate never requires a system `protoc`.

use std::path::PathBuf;

fn main() {
    // sdk/gantry-connect-proto -> ../../proto
    let proto_root = PathBuf::from(env!("CARGO_MANIFEST_DIR"))
        .join("..")
        .join("..")
        .join("proto");

    let files = [
        proto_root.join("gantry/v1/telemetry.proto"),
        proto_root.join("gantry/v1/ingest.proto"),
        proto_root.join("gantry/v1/live.proto"),
    ];

    for f in &files {
        println!("cargo:rerun-if-changed={}", f.display());
    }
    println!("cargo:rerun-if-changed={}", proto_root.display());

    // protox compiles to a FileDescriptorSet in-process (no protoc).
    let fds =
        protox::compile(files, [proto_root]).expect("protox failed to compile gantry/v1 protos");

    // prost-build turns the descriptors into Rust; compile_fds does not shell out to protoc.
    let mut config = prost_build::Config::new();
    config
        .compile_fds(fds)
        .expect("prost-build failed to generate types from descriptors");
}
