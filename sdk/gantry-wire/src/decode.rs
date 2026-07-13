//! Incremental decoder (requires the `alloc` feature).
//!
//! Feed arbitrary byte chunks to [`Decoder::push`]; complete records are delivered to a sink
//! closure as owned [`Record`]s. The decoder resynchronizes on the `0x00` delimiter, so torn
//! or garbage input simply produces frames that fail COBS/CRC and are counted. It tracks the
//! `PacketDef` layouts it has seen so it can decode `Samples` values; a `Samples` record for
//! an unregistered `packet_id` is counted and dropped (per `docs/WIRE.md`, defs re-send so the
//! gap self-heals).

use alloc::collections::BTreeMap;
use alloc::string::String;
use alloc::vec::Vec;

use crate::{crc, record_type, Kind};

/// A decoded field value.
#[derive(Clone, PartialEq, Debug)]
pub enum Value {
    F32(f32),
    F64(f64),
    I32(i32),
    I64(i64),
    Bool(bool),
    Str(String),
    Bytes(Vec<u8>),
}

/// A decoded `PacketDef` field descriptor.
#[derive(Clone, PartialEq, Eq, Debug)]
pub struct Field {
    pub name: String,
    pub kind: Kind,
    pub unit: String,
}

/// A fully validated record handed out by the [`Decoder`].
#[derive(Clone, PartialEq, Debug)]
pub enum Record {
    DeviceInfo {
        device_id: String,
        session: u32,
        tick_hz: u64,
    },
    PacketDef {
        packet_id: u64,
        name: String,
        fields: Vec<Field>,
    },
    Samples {
        packet_id: u64,
        ticks: u64,
        values: Vec<Value>,
    },
    TimeSync {
        ticks: u64,
        seq: u64,
    },
    Stats {
        frames_dropped: u64,
        records_sent: u64,
    },
    /// A well-framed, CRC-valid record whose type id is not known to this decoder. Carried
    /// through (rather than silently dropped) so callers can observe forward-compat traffic.
    Unknown {
        type_id: u8,
    },
}

/// Incremental, resynchronizing wire decoder.
#[derive(Default)]
pub struct Decoder {
    /// COBS bytes of the frame currently being accumulated (between delimiters).
    frame: Vec<u8>,
    /// Reusable COBS-decode scratch.
    scratch: Vec<u8>,
    /// Field layouts of `PacketDef`s seen this session, keyed by `packet_id`.
    packets: BTreeMap<u64, Vec<Kind>>,
    session: Option<u32>,
    crc_failures: u64,
    malformed: u64,
    dropped_samples: u64,
    unknown_records: u64,
}

impl Decoder {
    /// A fresh decoder with no state.
    pub fn new() -> Self {
        Self::default()
    }

    /// CRC-16 failures counted so far.
    pub fn crc_failures(&self) -> u64 {
        self.crc_failures
    }

    /// Frames that failed COBS decode or ran out of bytes mid-parse.
    pub fn malformed(&self) -> u64 {
        self.malformed
    }

    /// `Samples` records dropped because their `packet_id` had no seen `PacketDef`.
    pub fn dropped_samples(&self) -> u64 {
        self.dropped_samples
    }

    /// Count of well-framed records with an unknown type id.
    pub fn unknown_records(&self) -> u64 {
        self.unknown_records
    }

    /// Feed a chunk of bytes; every complete record is delivered to `sink`.
    pub fn push<F: FnMut(Record)>(&mut self, data: &[u8], mut sink: F) {
        for &b in data {
            if b == 0 {
                if !self.frame.is_empty() {
                    self.process_frame(&mut sink);
                    self.frame.clear();
                }
            } else {
                self.frame.push(b);
            }
        }
    }

    /// Convenience for tests/tools: decode a chunk and collect records into a `Vec`.
    pub fn push_to_vec(&mut self, data: &[u8]) -> Vec<Record> {
        let mut out = Vec::new();
        self.push(data, |r| out.push(r));
        out
    }

    fn process_frame<F: FnMut(Record)>(&mut self, sink: &mut F) {
        // Move the scratch buffer out so the parse helpers can borrow `self` mutably (registry,
        // counters) while a `Reader` borrows the decoded bytes. Put it back to reuse capacity.
        let mut data = core::mem::take(&mut self.scratch);
        data.clear();
        if cobs_decode(&self.frame, &mut data).is_err() {
            self.malformed += 1;
        } else {
            self.decode_valid(&data, sink);
        }
        self.scratch = data;
    }

    fn decode_valid<F: FnMut(Record)>(&mut self, data: &[u8], sink: &mut F) {
        // Need at least type(1) + crc(2).
        if data.len() < 3 {
            self.malformed += 1;
            return;
        }
        let n = data.len();
        let got = (data[n - 2] as u16) | ((data[n - 1] as u16) << 8);
        let body = &data[..n - 2];
        if crc::checksum(body) != got {
            self.crc_failures += 1;
            return;
        }
        let type_id = body[0];
        let mut r = Reader::new(&body[1..]);
        let parsed = match type_id {
            record_type::DEVICE_INFO => self.parse_device_info(&mut r),
            record_type::PACKET_DEF => self.parse_packet_def(&mut r),
            record_type::SAMPLES => self.parse_samples(&mut r),
            record_type::TIME_SYNC => self.parse_time_sync(&mut r),
            record_type::STATS => self.parse_stats(&mut r),
            other => {
                self.unknown_records += 1;
                Some(Some(Record::Unknown { type_id: other }))
            }
        };
        match parsed {
            // Parsed cleanly and yielded a record.
            Some(Some(rec)) => sink(rec),
            // Parsed cleanly but intentionally yielded nothing (e.g. dropped orphan Samples).
            Some(None) => {}
            // Ran out of bytes / invalid content.
            None => self.malformed += 1,
        }
    }

    fn parse_device_info(&mut self, r: &mut Reader<'_>) -> Option<Option<Record>> {
        let device_id = r.str()?;
        let session = r.u32_le()?;
        let tick_hz = r.varint()?;
        // A new session resets all learned packet layouts.
        if self.session != Some(session) {
            self.packets.clear();
            self.session = Some(session);
        }
        Some(Some(Record::DeviceInfo {
            device_id,
            session,
            tick_hz,
        }))
    }

    fn parse_packet_def(&mut self, r: &mut Reader<'_>) -> Option<Option<Record>> {
        let packet_id = r.varint()?;
        let name = r.str()?;
        let count = r.varint()?;
        let mut fields = Vec::new();
        let mut kinds = Vec::new();
        for _ in 0..count {
            let fname = r.str()?;
            let kind = Kind::from_u8(r.u8()?)?;
            let unit = r.str()?;
            kinds.push(kind);
            fields.push(Field {
                name: fname,
                kind,
                unit,
            });
        }
        self.packets.insert(packet_id, kinds);
        Some(Some(Record::PacketDef {
            packet_id,
            name,
            fields,
        }))
    }

    fn parse_samples(&mut self, r: &mut Reader<'_>) -> Option<Option<Record>> {
        let packet_id = r.varint()?;
        let ticks = r.varint()?;
        let kinds = match self.packets.get(&packet_id) {
            Some(k) => k.clone(),
            None => {
                // Orphan Samples: cleanly parsed header but no layout — drop and count.
                self.dropped_samples += 1;
                return Some(None);
            }
        };
        let mut values = Vec::with_capacity(kinds.len());
        for kind in &kinds {
            let v = match kind {
                Kind::F32 => Value::F32(r.f32()?),
                Kind::F64 => Value::F64(r.f64()?),
                Kind::I32 => Value::I32(r.zigzag()? as i32),
                Kind::I64 => Value::I64(r.zigzag()?),
                Kind::Bool => Value::Bool(r.u8()? != 0),
                Kind::Str => Value::Str(r.str()?),
                Kind::Bytes => Value::Bytes(r.bytes()?),
            };
            values.push(v);
        }
        Some(Some(Record::Samples {
            packet_id,
            ticks,
            values,
        }))
    }

    fn parse_time_sync(&mut self, r: &mut Reader<'_>) -> Option<Option<Record>> {
        let ticks = r.varint()?;
        let seq = r.varint()?;
        Some(Some(Record::TimeSync { ticks, seq }))
    }

    fn parse_stats(&mut self, r: &mut Reader<'_>) -> Option<Option<Record>> {
        let frames_dropped = r.varint()?;
        let records_sent = r.varint()?;
        Some(Some(Record::Stats {
            frames_dropped,
            records_sent,
        }))
    }
}

/// Decode a COBS frame (no delimiter) into `dst`. Returns `Err` on a malformed frame
/// (embedded zero code, or a run that overshoots the buffer).
fn cobs_decode(src: &[u8], dst: &mut Vec<u8>) -> Result<(), ()> {
    let mut i = 0;
    while i < src.len() {
        let code = src[i];
        i += 1;
        if code == 0 {
            return Err(()); // 0 never appears inside a COBS frame
        }
        for _ in 1..code {
            let b = *src.get(i).ok_or(())?;
            dst.push(b);
            i += 1;
        }
        if code != 0xFF && i < src.len() {
            dst.push(0);
        }
    }
    Ok(())
}

/// Bounds-checked cursor over a decoded record body.
struct Reader<'a> {
    buf: &'a [u8],
    pos: usize,
}

impl<'a> Reader<'a> {
    fn new(buf: &'a [u8]) -> Self {
        Reader { buf, pos: 0 }
    }

    fn u8(&mut self) -> Option<u8> {
        let b = *self.buf.get(self.pos)?;
        self.pos += 1;
        Some(b)
    }

    fn u32_le(&mut self) -> Option<u32> {
        let mut v = 0u32;
        for i in 0..4 {
            v |= (self.u8()? as u32) << (8 * i);
        }
        Some(v)
    }

    fn varint(&mut self) -> Option<u64> {
        let mut result = 0u64;
        let mut shift = 0u32;
        loop {
            let byte = self.u8()?;
            if shift >= 64 {
                return None; // overlong varint
            }
            result |= ((byte & 0x7f) as u64) << shift;
            if byte & 0x80 == 0 {
                return Some(result);
            }
            shift += 7;
        }
    }

    fn zigzag(&mut self) -> Option<i64> {
        let v = self.varint()?;
        Some(((v >> 1) as i64) ^ -((v & 1) as i64))
    }

    fn f32(&mut self) -> Option<f32> {
        let mut b = [0u8; 4];
        for slot in &mut b {
            *slot = self.u8()?;
        }
        Some(f32::from_le_bytes(b))
    }

    fn f64(&mut self) -> Option<f64> {
        let mut b = [0u8; 8];
        for slot in &mut b {
            *slot = self.u8()?;
        }
        Some(f64::from_le_bytes(b))
    }

    fn str(&mut self) -> Option<String> {
        let bytes = self.bytes()?;
        String::from_utf8(bytes).ok()
    }

    fn bytes(&mut self) -> Option<Vec<u8>> {
        let len = self.varint()? as usize;
        if self.pos + len > self.buf.len() {
            return None;
        }
        let out = self.buf[self.pos..self.pos + len].to_vec();
        self.pos += len;
        Some(out)
    }
}
