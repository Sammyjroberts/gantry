//! Compile-fail (and pass) coverage for `#[derive(Telemetry)]` via trybuild.
//! Run with `--features enabled` (the derive only generates code when enabled).

#![cfg(feature = "enabled")]

#[test]
fn ui() {
    let t = trybuild::TestCases::new();
    t.pass("tests/ui/pass.rs");
    t.compile_fail("tests/ui/unsupported_type.rs");
    t.compile_fail("tests/ui/tuple_struct.rs");
    t.compile_fail("tests/ui/bad_attr.rs");
}
