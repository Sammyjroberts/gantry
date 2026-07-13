//! # gantry-wire — the device wire format (v0)
//!
//! Implements `docs/WIRE.md`: the byte protocol between a constrained device (MCU) and a
//! collector. Every record is `type:1 | payload:N | crc16:2`, then COBS-encoded and
//! terminated with a `0x00` delimiter. Multi-byte integers are little-endian, `varint` is
//! unsigned LEB128, signed ints use zigzag.
//!
//! ## Two halves
//!
//! * **Encoder** ([`RecordWriter`] + the `encode_*` helpers): `no_std`, **zero heap**. It
//!   streams COBS + CRC directly into a caller-provided `&mut [u8]`, so there is no scratch
//!   buffer and no allocation. This is what the [`gantry-tlm`](../gantry_tlm) hot path calls.
//! * **Decoder** ([`Decoder`], behind the `alloc` feature): incremental. Feed arbitrary byte
//!   chunks; it yields validated [`Record`]s with **owned** payloads (`String`/`Vec`), skips
//!   unknown record types, and counts CRC failures / malformed frames / orphan samples. This
//!   is what the serial agent and all round-trip tests use.
//!
//! ## `no_std`
//!
//! `#![no_std]` always. The encoder needs only `core`. The decoder pulls in `alloc` (owned
//! payloads live in a `Vec`/`String` and the packet registry is a `BTreeMap`). Build the
//! encoder-only, zero-alloc configuration with `--no-default-features`.

#![no_std]
#![forbid(unsafe_code)]

#[cfg(feature = "alloc")]
extern crate alloc;

mod crc;
mod encode;

pub use encode::{
    encode_device_info, encode_packet_def, encode_samples_header, encode_stats, encode_time_sync,
    RecordWriter, WireError,
};

#[cfg(feature = "alloc")]
mod decode;
#[cfg(feature = "alloc")]
pub use decode::{Decoder, Field, Record, Value};

/// Record type ids (see `docs/WIRE.md`). `0x00` is reserved as the COBS delimiter.
pub mod record_type {
    pub const DEVICE_INFO: u8 = 0x01;
    pub const PACKET_DEF: u8 = 0x02;
    pub const SAMPLES: u8 = 0x03;
    pub const TIME_SYNC: u8 = 0x04;
    pub const STATS: u8 = 0x05;
}

/// Field/value kind tag, as stored in `PacketDef` field descriptors and used to drive
/// `Samples` value encoding. Values match the on-wire `kind: u8` byte.
#[derive(Clone, Copy, PartialEq, Eq, Debug, Hash)]
#[repr(u8)]
pub enum Kind {
    F32 = 1,
    F64 = 2,
    I32 = 3,
    I64 = 4,
    Bool = 5,
    Str = 6,
    Bytes = 7,
}

impl Kind {
    /// The on-wire byte for this kind.
    #[inline]
    pub const fn to_u8(self) -> u8 {
        self as u8
    }

    /// Parse an on-wire kind byte. Unknown values yield `None`.
    #[inline]
    pub const fn from_u8(b: u8) -> Option<Kind> {
        match b {
            1 => Some(Kind::F32),
            2 => Some(Kind::F64),
            3 => Some(Kind::I32),
            4 => Some(Kind::I64),
            5 => Some(Kind::Bool),
            6 => Some(Kind::Str),
            7 => Some(Kind::Bytes),
            _ => None,
        }
    }
}

/// A field descriptor for [`encode_packet_def`]: name + kind + unit (unit may be empty).
#[derive(Clone, Copy, Debug)]
pub struct FieldDef<'a> {
    pub name: &'a str,
    pub kind: Kind,
    pub unit: &'a str,
}
