//! `#[derive(Telemetry)]` for `gantry-tlm`.
//!
//! When the `enabled` feature is **off** (the default; `gantry-tlm/enabled` forwards it on),
//! the derive expands to an empty token stream — no trait impl, no statics, nothing. That is
//! what makes telemetry genuinely zero-cost when compiled out: there is no generated code to
//! optimize away.

use proc_macro::TokenStream;

#[cfg(feature = "enabled")]
mod expand;

/// Derive the `gantry_tlm::Telemetry` trait for a struct with named fields.
///
/// Field attributes:
/// * `#[tlm(unit = "deg")]` — sets the field's unit string (default empty).
/// * `#[tlm(name = "pitch_deg")]` — overrides the on-wire field name (default: the field ident).
///
/// Struct attribute:
/// * `#[tlm(packet = "imu")]` — overrides the packet name (default: the struct name, snake_cased).
///
/// Supported field types: `f32`, `f64`, `bool`, and the integer types `i8`/`i16`/`i32`/`i64`
/// and `u8`/`u16`/`u32` (widened to `i64` on the wire). `&str` and other types are rejected at
/// compile time (owned heapless strings in structs are out of scope for v1).
#[proc_macro_derive(Telemetry, attributes(tlm))]
pub fn derive_telemetry(input: TokenStream) -> TokenStream {
    #[cfg(feature = "enabled")]
    {
        expand::expand(input)
    }
    #[cfg(not(feature = "enabled"))]
    {
        // Telemetry is compiled out: emit nothing at all.
        let _ = input;
        TokenStream::new()
    }
}
