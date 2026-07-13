//! The global telemetry pipeline (only compiled with the `enabled` feature).
//!
//! State lives in one `critical_section::Mutex<RefCell<Option<Pipeline>>>`. The hot path
//! (`send`, `tlm!`) takes a short critical section, encodes one wire record into a fixed stack
//! scratch buffer via [`gantry_wire`], then copies it into a byte-FIFO ring. On overflow the
//! oldest whole record (delimited by `0x00`) is evicted and counted. Nothing here allocates.

use core::cell::{RefCell, UnsafeCell};
use core::sync::atomic::{AtomicBool, AtomicU32, Ordering};

use critical_section::Mutex;
use gantry_wire::{encode_device_info, encode_stats, encode_time_sync, record_type, RecordWriter};

use crate::{FieldDesc, Kind, Telemetry, ValueWriter};

/// Max number of distinct packet types remembered for periodic `PacketDef` re-sends. Packets
/// beyond this still send their def once, just not on the re-send cadence.
const MAX_PACKETS: usize = 32;
/// Stack scratch for encoding one record. Records larger than this are dropped and counted.
const MAX_RECORD: usize = 256;

/// Cadences, in ticks, derived from `tick_hz` (see `docs/WIRE.md`).
const TIMESYNC_SECS: u64 = 1;
const STATS_SECS: u64 = 5;
const DEVICEINFO_SECS: u64 = 10;

static PIPELINE: Mutex<RefCell<Option<Pipeline>>> = Mutex::new(RefCell::new(None));

/// Static storage for the ring buffer, handed out once by [`RingCell::take`]. The `init!` macro
/// declares one of these so the RAM lives in `.bss`, sized by the app.
pub struct RingCell<const N: usize> {
    taken: AtomicBool,
    cell: UnsafeCell<[u8; N]>,
}

// SAFETY: `take` hands out the interior `&mut` exactly once (guarded by `taken`), after which
// the buffer is owned by the pipeline behind the critical-section mutex.
unsafe impl<const N: usize> Sync for RingCell<N> {}

impl<const N: usize> RingCell<N> {
    pub const fn new() -> Self {
        Self {
            taken: AtomicBool::new(false),
            cell: UnsafeCell::new([0; N]),
        }
    }

    /// Take the `&'static mut [u8]` backing store. Panics if called more than once.
    // Handing a unique `&mut` out of a shared `&` is the whole point of this cell (cf.
    // `StaticCell`); the `taken` flag makes it sound.
    #[allow(clippy::mut_from_ref)]
    pub fn take(&'static self) -> &'static mut [u8] {
        if self.taken.swap(true, Ordering::AcqRel) {
            panic!("gantry-tlm: init! ring taken twice");
        }
        // SAFETY: the swap above guarantees this is the only live `&mut`, and `&'static self`
        // means the storage lives forever. `cell` holds exactly `N` initialized bytes.
        unsafe { core::slice::from_raw_parts_mut(self.cell.get().cast::<u8>(), N) }
    }
}

impl<const N: usize> Default for RingCell<N> {
    fn default() -> Self {
        Self::new()
    }
}

/// Byte-FIFO of concatenated framed records (each ends in a `0x00` delimiter).
struct Ring {
    buf: &'static mut [u8],
    head: usize,
    len: usize,
}

impl Ring {
    fn cap(&self) -> usize {
        self.buf.len()
    }
    fn free(&self) -> usize {
        self.cap() - self.len
    }

    /// Append bytes; the caller must have ensured `free() >= data.len()`.
    fn push(&mut self, data: &[u8]) {
        let cap = self.cap();
        for &b in data {
            let idx = (self.head + self.len) % cap;
            self.buf[idx] = b;
            self.len += 1;
        }
    }

    /// Length (incl. delimiter) of the record at the head, or `None` if empty.
    fn peek_record_len(&self) -> Option<usize> {
        if self.len == 0 {
            return None;
        }
        let cap = self.cap();
        for i in 0..self.len {
            if self.buf[(self.head + i) % cap] == 0 {
                return Some(i + 1);
            }
        }
        None // unreachable: every pushed record ends in 0x00
    }

    /// Drop the head record. Returns its length.
    fn pop_record(&mut self) -> Option<usize> {
        let n = self.peek_record_len()?;
        self.head = (self.head + n) % self.cap();
        self.len -= n;
        Some(n)
    }

    /// Copy the head record into `dst` (which must be large enough). Returns its length.
    fn copy_record_out(&self, dst: &mut [u8]) -> usize {
        let n = self.peek_record_len().unwrap_or(0);
        let cap = self.cap();
        for (i, slot) in dst.iter_mut().enumerate().take(n) {
            *slot = self.buf[(self.head + i) % cap];
        }
        n
    }
}

/// A registered packet, remembered so its `PacketDef` can be re-sent periodically.
#[derive(Clone, Copy)]
enum PacketMeta {
    Derived {
        id: u32,
        name: &'static str,
        fields: &'static [FieldDesc],
    },
    Adhoc {
        id: u32,
        name: &'static str,
        field: FieldDesc,
    },
}

struct Pipeline {
    ring: Ring,
    clock: fn() -> u64,
    tick_hz: u64,
    device_id: &'static str,
    session: u32,
    /// Bumped on every re-init so stale per-type id cells are re-registered.
    generation: u8,
    next_id: u32,
    packets: [Option<PacketMeta>; MAX_PACKETS],
    npackets: usize,
    dropped: u64,
    sent: u64,
    timesync_seq: u64,
    last_timesync: u64,
    last_stats: u64,
    last_deviceinfo: u64,
}

/// Adapts the wire [`RecordWriter`] to the [`ValueWriter`] visitor.
struct RingSink<'a, 'b> {
    w: &'a mut RecordWriter<'b>,
}

impl ValueWriter for RingSink<'_, '_> {
    fn field_f32(&mut self, v: f32) {
        self.w.put_f32(v);
    }
    fn field_f64(&mut self, v: f64) {
        self.w.put_f64(v);
    }
    fn field_i32(&mut self, v: i32) {
        self.w.put_zigzag(v as i64);
    }
    fn field_i64(&mut self, v: i64) {
        self.w.put_zigzag(v);
    }
    fn field_bool(&mut self, v: bool) {
        self.w.put_bool(v);
    }
    fn field_str(&mut self, v: &str) {
        self.w.put_str(v);
    }
}

impl Pipeline {
    #[allow(clippy::too_many_arguments)]
    fn new(
        ring: &'static mut [u8],
        clock: fn() -> u64,
        tick_hz: u64,
        device_id: &'static str,
        session: u32,
        generation: u8,
    ) -> Self {
        Pipeline {
            ring: Ring {
                buf: ring,
                head: 0,
                len: 0,
            },
            clock,
            tick_hz,
            device_id,
            session,
            generation,
            next_id: 1,
            packets: [None; MAX_PACKETS],
            npackets: 0,
            dropped: 0,
            sent: 0,
            timesync_seq: 0,
            last_timesync: 0,
            last_stats: 0,
            last_deviceinfo: 0,
        }
    }

    /// Queue framed bytes, evicting oldest whole records (counted) until they fit.
    fn queue_bytes(&mut self, framed: &[u8]) {
        let n = framed.len();
        if n == 0 || n > self.ring.cap() {
            self.dropped += 1;
            return;
        }
        while self.ring.free() < n {
            if self.ring.pop_record().is_some() {
                self.dropped += 1;
            } else {
                break;
            }
        }
        if self.ring.free() >= n {
            self.ring.push(framed);
            self.sent += 1;
        } else {
            self.dropped += 1;
        }
    }

    fn emit_device_info(&mut self) {
        let (dev, sess, hz) = (self.device_id, self.session, self.tick_hz);
        let mut scratch = [0u8; MAX_RECORD];
        if let Ok(n) = encode_device_info(&mut scratch, dev, sess, hz) {
            self.queue_bytes(&scratch[..n]);
        }
    }

    fn emit_time_sync(&mut self, ticks: u64, seq: u64) {
        let mut scratch = [0u8; MAX_RECORD];
        if let Ok(n) = encode_time_sync(&mut scratch, ticks, seq) {
            self.queue_bytes(&scratch[..n]);
        }
    }

    fn emit_stats(&mut self) {
        let (d, s) = (self.dropped, self.sent);
        let mut scratch = [0u8; MAX_RECORD];
        if let Ok(n) = encode_stats(&mut scratch, d, s) {
            self.queue_bytes(&scratch[..n]);
        }
    }

    fn emit_packet_def(&mut self, meta: PacketMeta) {
        let mut scratch = [0u8; MAX_RECORD];
        let mut w = RecordWriter::new(&mut scratch, record_type::PACKET_DEF);
        match meta {
            PacketMeta::Derived { id, name, fields } => {
                w.put_varint(id as u64);
                w.put_str(name);
                w.put_varint(fields.len() as u64);
                for f in fields {
                    w.put_str(f.name);
                    w.put_u8(f.kind.to_u8());
                    w.put_str(f.unit);
                }
            }
            PacketMeta::Adhoc { id, name, field } => {
                w.put_varint(id as u64);
                w.put_str(name);
                w.put_varint(1);
                w.put_str(field.name);
                w.put_u8(field.kind.to_u8());
                w.put_str(field.unit);
            }
        }
        if let Ok(n) = w.finish() {
            self.queue_bytes(&scratch[..n]);
        }
    }

    fn add_packet(&mut self, meta: PacketMeta) {
        if self.npackets < MAX_PACKETS {
            self.packets[self.npackets] = Some(meta);
            self.npackets += 1;
        }
        self.emit_packet_def(meta);
    }

    /// Assign a fresh packet id, packing the current generation into the top byte.
    fn assign_id(&mut self, cell: &AtomicU32) -> Option<u32> {
        let id = self.next_id;
        if id > 0x00FF_FFFF {
            return None; // out of ids
        }
        self.next_id += 1;
        cell.store(((self.generation as u32) << 24) | id, Ordering::Relaxed);
        Some(id)
    }

    fn lookup(&self, cell: &AtomicU32) -> Option<u32> {
        let packed = cell.load(Ordering::Relaxed);
        if packed != 0 && (packed >> 24) as u8 == self.generation {
            Some(packed & 0x00FF_FFFF)
        } else {
            None
        }
    }

    fn resolve_derived<T: Telemetry>(&mut self, cell: &AtomicU32) -> Option<u32> {
        if let Some(id) = self.lookup(cell) {
            return Some(id);
        }
        let id = self.assign_id(cell)?;
        self.add_packet(PacketMeta::Derived {
            id,
            name: T::PACKET,
            fields: T::FIELDS,
        });
        Some(id)
    }

    fn resolve_adhoc(
        &mut self,
        cell: &AtomicU32,
        field_name: &'static str,
        kind: Kind,
    ) -> Option<u32> {
        if let Some(id) = self.lookup(cell) {
            return Some(id);
        }
        let id = self.assign_id(cell)?;
        self.add_packet(PacketMeta::Adhoc {
            id,
            name: "adhoc",
            field: FieldDesc {
                name: field_name,
                kind,
                unit: "",
            },
        });
        Some(id)
    }

    /// Emit periodic records whose cadence has elapsed (called from `drain`).
    fn maybe_emit_periodics(&mut self) {
        if self.tick_hz == 0 {
            return;
        }
        let now = (self.clock)();
        let hz = self.tick_hz;

        if now.saturating_sub(self.last_timesync) >= hz.saturating_mul(TIMESYNC_SECS) {
            self.last_timesync = now;
            self.timesync_seq += 1;
            let seq = self.timesync_seq;
            self.emit_time_sync(now, seq);
        }
        if now.saturating_sub(self.last_stats) >= hz.saturating_mul(STATS_SECS) {
            self.last_stats = now;
            self.emit_stats();
        }
        if now.saturating_sub(self.last_deviceinfo) >= hz.saturating_mul(DEVICEINFO_SECS) {
            self.last_deviceinfo = now;
            self.emit_device_info();
            for i in 0..self.npackets {
                if let Some(meta) = self.packets[i] {
                    self.emit_packet_def(meta);
                }
            }
        }
    }

    fn drain_into(&mut self, buf: &mut [u8]) -> usize {
        let mut written = 0;
        while let Some(rlen) = self.ring.peek_record_len() {
            if written + rlen > buf.len() {
                break;
            }
            self.ring.copy_record_out(&mut buf[written..]);
            self.ring.pop_record();
            written += rlen;
        }
        written
    }
}

/// Drain-side stats snapshot.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct DrainStats {
    pub records_sent: u64,
    pub frames_dropped: u64,
}

/// Install the global pipeline. Called by [`init!`]; not meant to be called directly.
pub fn __init(
    ring: &'static mut [u8],
    clock: fn() -> u64,
    tick_hz: u64,
    device_id: &'static str,
    session: u32,
) {
    critical_section::with(|cs| {
        let cell = PIPELINE.borrow(cs);
        let mut guard = cell.borrow_mut();
        let generation = guard
            .as_ref()
            .map(|p| p.generation.wrapping_add(1))
            .unwrap_or(0);
        let mut p = Pipeline::new(ring, clock, tick_hz, device_id, session, generation);
        // DeviceInfo goes out first so late joiners (and the decoder) sync before any Samples.
        p.emit_device_info();
        let now = (p.clock)();
        p.last_timesync = now;
        p.last_stats = now;
        p.last_deviceinfo = now;
        *guard = Some(p);
    });
}

/// Send a telemetry packet: registers it on first sight, then encodes a `Samples` record.
pub fn send<T: Telemetry>(v: &T) {
    critical_section::with(|cs| {
        let cell = PIPELINE.borrow(cs);
        let mut guard = cell.borrow_mut();
        let Some(p) = guard.as_mut() else {
            return;
        };
        let Some(id) = p.resolve_derived::<T>(T::id_cell()) else {
            return;
        };
        let ticks = (p.clock)();
        let mut scratch = [0u8; MAX_RECORD];
        let mut w = gantry_wire::encode_samples_header(&mut scratch, id, ticks);
        {
            let mut sink = RingSink { w: &mut w };
            v.write_values(&mut sink);
        }
        match w.finish() {
            Ok(n) => p.queue_bytes(&scratch[..n]),
            Err(_) => p.dropped += 1,
        }
    });
}

/// Ad-hoc single-field send. Called by [`tlm!`]; not meant to be called directly.
pub fn __adhoc<V: crate::AdhocValue>(cell: &'static AtomicU32, field_name: &'static str, v: V) {
    critical_section::with(|cs| {
        let pcell = PIPELINE.borrow(cs);
        let mut guard = pcell.borrow_mut();
        let Some(p) = guard.as_mut() else {
            return;
        };
        let Some(id) = p.resolve_adhoc(cell, field_name, V::KIND) else {
            return;
        };
        let ticks = (p.clock)();
        let mut scratch = [0u8; MAX_RECORD];
        let mut w = gantry_wire::encode_samples_header(&mut scratch, id, ticks);
        {
            let mut sink = RingSink { w: &mut w };
            v.write(&mut sink);
        }
        match w.finish() {
            Ok(n) => p.queue_bytes(&scratch[..n]),
            Err(_) => p.dropped += 1,
        }
    });
}

/// Emit any due periodic records, then hand out complete framed records into `buf`.
/// Returns the number of bytes written. `buf` should be at least as large as one record
/// (records that don't fit are left queued).
pub fn drain(buf: &mut [u8]) -> usize {
    critical_section::with(|cs| {
        let cell = PIPELINE.borrow(cs);
        let mut guard = cell.borrow_mut();
        let Some(p) = guard.as_mut() else {
            return 0;
        };
        p.maybe_emit_periodics();
        p.drain_into(buf)
    })
}

/// Snapshot of records-sent / frames-dropped counters.
pub fn stats() -> DrainStats {
    critical_section::with(|cs| {
        let cell = PIPELINE.borrow(cs);
        let guard = cell.borrow_mut();
        guard
            .as_ref()
            .map(|p| DrainStats {
                records_sent: p.sent,
                frames_dropped: p.dropped,
            })
            .unwrap_or_default()
    })
}
