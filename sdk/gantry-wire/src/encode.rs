//! Zero-alloc record encoder.
//!
//! [`RecordWriter`] streams COBS + CRC-16 directly into a caller-provided buffer — there is
//! no scratch buffer and no heap. Bytes fed via the `put_*` methods form the record payload
//! (and update the running CRC); on [`RecordWriter::finish`] the two little-endian CRC bytes
//! and the `0x00` delimiter are appended and the total framed length is returned.

use crate::crc;
use crate::{record_type, FieldDef};

/// The only way encoding can fail: the caller's output buffer was too small for the framed
/// record. Nothing is partially emitted that a receiver would accept (the delimiter is the
/// last byte written, and it is only written on success).
#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub struct WireError;

impl core::fmt::Display for WireError {
    fn fmt(&self, f: &mut core::fmt::Formatter<'_>) -> core::fmt::Result {
        write!(f, "output buffer too small for framed record")
    }
}

/// Streaming writer for a single framed record.
///
/// Construct with [`RecordWriter::new`] (which emits the record-type byte), push the payload
/// with the `put_*` methods, then call [`RecordWriter::finish`]. Overflow is tracked
/// internally and reported once, at `finish`.
pub struct RecordWriter<'a> {
    out: &'a mut [u8],
    /// Next free index in `out`.
    pos: usize,
    /// Index of the pending COBS code byte.
    code_pos: usize,
    /// Bytes since the last code byte, plus one (the running COBS code).
    code: u8,
    /// Running CRC over type + payload (never the CRC bytes themselves).
    crc: u16,
    /// Cleared the first time a write does not fit.
    ok: bool,
}

impl<'a> RecordWriter<'a> {
    /// Begin a record of `record_type` writing into `out`. Emits the type byte immediately.
    pub fn new(out: &'a mut [u8], record_type: u8) -> Self {
        let mut w = RecordWriter {
            out,
            pos: 0,
            code_pos: 0,
            code: 1,
            crc: 0xFFFF,
            ok: true,
        };
        w.reserve_code();
        w.put_u8(record_type);
        w
    }

    /// Reserve the slot for the next COBS code byte.
    fn reserve_code(&mut self) {
        if self.pos < self.out.len() {
            self.code_pos = self.pos;
            self.out[self.pos] = 0; // placeholder; back-patched later
            self.pos += 1;
        } else {
            self.ok = false;
        }
    }

    /// Emit one already-COBS-encoded output byte.
    fn emit(&mut self, b: u8) {
        if self.pos < self.out.len() {
            self.out[self.pos] = b;
            self.pos += 1;
        } else {
            self.ok = false;
        }
    }

    /// Feed one byte through the streaming COBS encoder (no CRC update).
    fn cobs_push(&mut self, b: u8) {
        if !self.ok {
            return;
        }
        if b == 0 {
            self.out[self.code_pos] = self.code;
            self.reserve_code();
            self.code = 1;
        } else {
            self.emit(b);
            self.code += 1;
            if self.code == 0xFF {
                self.out[self.code_pos] = self.code;
                self.reserve_code();
                self.code = 1;
            }
        }
    }

    /// Feed one payload byte: update the CRC, then COBS-encode it.
    #[inline]
    fn raw(&mut self, b: u8) {
        self.crc = crc::update(self.crc, b);
        self.cobs_push(b);
    }

    /// Append a raw byte to the payload.
    #[inline]
    pub fn put_u8(&mut self, b: u8) {
        self.raw(b);
    }

    /// Append an unsigned LEB128 varint.
    pub fn put_varint(&mut self, mut v: u64) {
        loop {
            let mut byte = (v & 0x7f) as u8;
            v >>= 7;
            if v != 0 {
                byte |= 0x80;
            }
            self.raw(byte);
            if v == 0 {
                break;
            }
        }
    }

    /// Append a signed integer as a zigzag varint.
    #[inline]
    pub fn put_zigzag(&mut self, v: i64) {
        self.put_varint(((v << 1) ^ (v >> 63)) as u64);
    }

    /// Append an `f32` (4 little-endian bytes).
    #[inline]
    pub fn put_f32(&mut self, v: f32) {
        for b in v.to_le_bytes() {
            self.raw(b);
        }
    }

    /// Append an `f64` (8 little-endian bytes).
    #[inline]
    pub fn put_f64(&mut self, v: f64) {
        for b in v.to_le_bytes() {
            self.raw(b);
        }
    }

    /// Append a boolean (one byte, `0`/`1`).
    #[inline]
    pub fn put_bool(&mut self, v: bool) {
        self.raw(v as u8);
    }

    /// Append a string: varint length + UTF-8 bytes.
    pub fn put_str(&mut self, s: &str) {
        self.put_varint(s.len() as u64);
        for &b in s.as_bytes() {
            self.raw(b);
        }
    }

    /// Append a byte string: varint length + raw bytes.
    pub fn put_bytes(&mut self, b: &[u8]) {
        self.put_varint(b.len() as u64);
        for &x in b {
            self.raw(x);
        }
    }

    /// Finish the record: append the little-endian CRC, back-patch the final COBS code, and
    /// write the `0x00` delimiter. Returns the total framed length, or [`WireError`] if the
    /// output buffer was too small at any point.
    pub fn finish(mut self) -> Result<usize, WireError> {
        let lo = (self.crc & 0xff) as u8;
        let hi = (self.crc >> 8) as u8;
        self.cobs_push(lo);
        self.cobs_push(hi);
        if !self.ok {
            return Err(WireError);
        }
        self.out[self.code_pos] = self.code;
        self.emit(0); // delimiter
        if !self.ok {
            return Err(WireError);
        }
        Ok(self.pos)
    }
}

/// Encode a `DeviceInfo` (`0x01`) record into `out`. Returns the framed length.
pub fn encode_device_info(
    out: &mut [u8],
    device_id: &str,
    session: u32,
    tick_hz: u64,
) -> Result<usize, WireError> {
    let mut w = RecordWriter::new(out, record_type::DEVICE_INFO);
    w.put_str(device_id);
    // session is a fixed u32 little-endian field.
    for b in session.to_le_bytes() {
        w.put_u8(b);
    }
    w.put_varint(tick_hz);
    w.finish()
}

/// Encode a `PacketDef` (`0x02`) record into `out`. Returns the framed length.
pub fn encode_packet_def(
    out: &mut [u8],
    packet_id: u32,
    name: &str,
    fields: &[FieldDef<'_>],
) -> Result<usize, WireError> {
    let mut w = RecordWriter::new(out, record_type::PACKET_DEF);
    w.put_varint(packet_id as u64);
    w.put_str(name);
    w.put_varint(fields.len() as u64);
    for f in fields {
        w.put_str(f.name);
        w.put_u8(f.kind.to_u8());
        w.put_str(f.unit);
    }
    w.finish()
}

/// Begin a `Samples` (`0x03`) record: emits the header (`packet_id`, `ticks`) and returns the
/// open [`RecordWriter`] so the caller can `put_*` the field values in `PacketDef` order and
/// then call [`RecordWriter::finish`].
pub fn encode_samples_header(out: &mut [u8], packet_id: u32, ticks: u64) -> RecordWriter<'_> {
    let mut w = RecordWriter::new(out, record_type::SAMPLES);
    w.put_varint(packet_id as u64);
    w.put_varint(ticks);
    w
}

/// Encode a `TimeSync` (`0x04`) record into `out`. Returns the framed length.
pub fn encode_time_sync(out: &mut [u8], ticks: u64, seq: u64) -> Result<usize, WireError> {
    let mut w = RecordWriter::new(out, record_type::TIME_SYNC);
    w.put_varint(ticks);
    w.put_varint(seq);
    w.finish()
}

/// Encode a `Stats` (`0x05`) record into `out`. Returns the framed length.
pub fn encode_stats(
    out: &mut [u8],
    frames_dropped: u64,
    records_sent: u64,
) -> Result<usize, WireError> {
    let mut w = RecordWriter::new(out, record_type::STATS);
    w.put_varint(frames_dropped);
    w.put_varint(records_sent);
    w.finish()
}
