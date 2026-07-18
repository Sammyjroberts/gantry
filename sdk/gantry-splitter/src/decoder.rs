//! Stateful, passive Feetech STS3215 decoder — the heart of the splitter.
//!
//! The SO-101 bus is **half-duplex, request/response** (Dynamixel-protocol-1 style). A status
//! (device→host) packet carries *only* the data bytes — never the register address it answers. So
//! to attribute a `Present_Position` read we must remember the **last instruction** the host sent
//! on that port: `READ(id, addr, size)` / `SYNC_READ(addr, size, ids…)`. Writes carry their own
//! address, so `WRITE`/`SYNC_WRITE` of `Goal_Position` are decoded directly on the host→device
//! stream (lerobot drives the follower with `SYNC_WRITE`).
//!
//! The decoder is **pure and passive**: it is fed the two byte directions of one port (a copy/tee
//! of what the pump already forwarded) and returns [`Reading`]s. It never stalls the pump — decode
//! runs *after* the bytes are on the wire, and any framing/CRC problem is counted and resynced
//! (hunt for `FF FF`, checksum-verify), never propagated.
//!
//! Register addresses are verified against lerobot `motors/feetech/tables.py` (sts3215):
//! `Present_Position (56, 2)`, `Goal_Position (42, 2)`.

/// `Present_Position` control-table address (read-only, 2 bytes, 0..4095 over 360°).
pub const PRESENT_POSITION: u8 = 56;
/// `Goal_Position` control-table address (2 bytes) — the teleop command target.
pub const GOAL_POSITION: u8 = 42;

const INSTR_READ: u8 = 0x02;
const INSTR_WRITE: u8 = 0x03;
const INSTR_SYNC_READ: u8 = 0x82;
const INSTR_SYNC_WRITE: u8 = 0x83;

/// Which side of the port a byte stream came from. The host→device stream carries *instructions*;
/// the device→host stream carries *status* replies.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Direction {
    /// lerobot (the PTY master/host) → the servos.
    HostToDevice,
    /// The servos → lerobot.
    DeviceToHost,
}

/// The kind of value a [`Reading`] carries.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Channel {
    /// `Present_Position` — measured joint position (published as channel `pos`).
    Pos,
    /// `Goal_Position` — commanded joint target (published as channel `cmd`).
    Cmd,
}

/// One decoded position value, still as a **raw 16-bit servo count** (sign-magnitude, see
/// [`crate::calibration`]). The caller maps `servo_id` → joint name and normalizes.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct Reading {
    /// Feetech servo id (1..=6 on an SO-101).
    pub servo_id: u8,
    /// Whether this is a measured position or a commanded target.
    pub channel: Channel,
    /// Raw 16-bit little-endian value straight off the wire (pre-normalization).
    pub raw: u16,
}

/// The last read context the host established (so subsequent status packets can be attributed).
#[derive(Debug, Clone)]
struct Pending {
    addr: u8,
    size: u8,
    ids: Vec<u8>,
}

/// A parsed, checksum-valid frame (either an instruction or a status packet).
struct RawPacket {
    id: u8,
    /// Instruction byte (host→device) or error byte (device→host).
    kind: u8,
    /// Everything between `kind` and the checksum.
    params: Vec<u8>,
}

/// Feetech checksum: `(~sum) & 0xFF` over `[id, len, kind, params…]` (everything but the two
/// `FF FF` header bytes and the checksum byte itself).
#[inline]
pub fn checksum(body: &[u8]) -> u8 {
    !body.iter().fold(0u8, |acc, b| acc.wrapping_add(*b))
}

/// Decode a Feetech sign-magnitude 16-bit position (sign bit at index 15). Matches lerobot's
/// `decode_sign_magnitude(value, 15)`: bit 15 is the sign, bits 0..14 the magnitude.
#[inline]
pub fn decode_sign_magnitude_15(raw: u16) -> i32 {
    let magnitude = (raw & 0x7FFF) as i32;
    if raw & 0x8000 != 0 {
        -magnitude
    } else {
        magnitude
    }
}

/// One port's decoder: two independent framing buffers (one per direction) plus the shared
/// half-duplex read context.
#[derive(Default)]
pub struct PortDecoder {
    host_buf: Vec<u8>,
    dev_buf: Vec<u8>,
    pending: Option<Pending>,
    crc_failures: u64,
    malformed: u64,
}

impl PortDecoder {
    /// A fresh decoder.
    pub fn new() -> Self {
        Self::default()
    }

    /// Checksum-rejected packets seen so far.
    pub fn crc_failures(&self) -> u64 {
        self.crc_failures
    }

    /// Structurally malformed (e.g. impossible length) frames skipped so far.
    pub fn malformed(&self) -> u64 {
        self.malformed
    }

    /// Feed one direction's freshly-forwarded bytes; return any readings they completed.
    ///
    /// Tolerant of partial packets (leftover bytes are buffered until the next feed) and of
    /// interleaved noise (resync on the next `FF FF`, checksum-verified).
    pub fn feed(&mut self, dir: Direction, bytes: &[u8]) -> Vec<Reading> {
        let (packets, crc_d, mal_d) = {
            let buf = match dir {
                Direction::HostToDevice => &mut self.host_buf,
                Direction::DeviceToHost => &mut self.dev_buf,
            };
            buf.extend_from_slice(bytes);
            parse_buffer(buf)
        };
        self.crc_failures += crc_d;
        self.malformed += mal_d;

        let mut readings = Vec::new();
        for p in packets {
            match dir {
                Direction::HostToDevice => self.on_instruction(p, &mut readings),
                Direction::DeviceToHost => self.on_status(p, &mut readings),
            }
        }
        readings
    }

    fn on_instruction(&mut self, p: RawPacket, readings: &mut Vec<Reading>) {
        match p.kind {
            INSTR_READ => {
                // params = [addr, size]
                if p.params.len() >= 2 {
                    self.pending = Some(Pending {
                        addr: p.params[0],
                        size: p.params[1],
                        ids: vec![p.id],
                    });
                }
            }
            INSTR_SYNC_READ => {
                // params = [addr, size, id0, id1, ...]
                if p.params.len() >= 2 {
                    self.pending = Some(Pending {
                        addr: p.params[0],
                        size: p.params[1],
                        ids: p.params[2..].to_vec(),
                    });
                }
            }
            INSTR_WRITE => {
                // params = [addr, data0, data1, ...]
                if p.params.len() >= 3 && p.params[0] == GOAL_POSITION {
                    let raw = u16::from_le_bytes([p.params[1], p.params[2]]);
                    readings.push(Reading {
                        servo_id: p.id,
                        channel: Channel::Cmd,
                        raw,
                    });
                }
            }
            INSTR_SYNC_WRITE => {
                // params = [addr, len_per, (id, data[len_per])* ]
                if p.params.len() >= 2 && p.params[0] == GOAL_POSITION {
                    let per = p.params[1] as usize;
                    let mut i = 2;
                    while i + 1 + per <= p.params.len() {
                        let motor_id = p.params[i];
                        if per >= 2 {
                            let raw = u16::from_le_bytes([p.params[i + 1], p.params[i + 2]]);
                            readings.push(Reading {
                                servo_id: motor_id,
                                channel: Channel::Cmd,
                                raw,
                            });
                        }
                        i += 1 + per;
                    }
                }
            }
            _ => { /* PING / WRITE to other regs / unknown: nothing to attribute */ }
        }
    }

    fn on_status(&mut self, p: RawPacket, readings: &mut Vec<Reading>) {
        // A status packet is `id, err, data…, cksum`; `p.kind` is the error byte, `p.params` the
        // data. Attribute it to the pending read only when that read targeted Present_Position and
        // named this servo id.
        let Some(pending) = self.pending.as_ref() else {
            return;
        };
        if pending.addr == PRESENT_POSITION
            && pending.ids.contains(&p.id)
            && p.params.len() >= 2
            && pending.size >= 2
        {
            let raw = u16::from_le_bytes([p.params[0], p.params[1]]);
            readings.push(Reading {
                servo_id: p.id,
                channel: Channel::Pos,
                raw,
            });
        }
    }
}

/// Extract every complete, checksum-valid packet at the front of `buf`, draining consumed bytes and
/// leaving any partial tail. Returns `(packets, crc_failures, malformed)`.
///
/// Resync policy: on a bad checksum or impossible length, drop a single leading `FF` and re-hunt —
/// so a genuine `FF FF` later in the buffer is still found.
fn parse_buffer(buf: &mut Vec<u8>) -> (Vec<RawPacket>, u64, u64) {
    let mut out = Vec::new();
    let mut crc = 0u64;
    let mut malformed = 0u64;

    loop {
        let Some(start) = find_header(buf) else {
            // No header. Keep only a possible partial-header trailing 0xFF; drop the rest.
            if !buf.is_empty() {
                let keep_ff = *buf.last().unwrap() == 0xFF;
                buf.clear();
                if keep_ff {
                    buf.push(0xFF);
                }
            }
            break;
        };
        if start > 0 {
            buf.drain(0..start);
        }
        // buf now starts with FF FF.
        if buf.len() < 4 {
            break; // need id + len
        }
        let id = buf[2];
        let len = buf[3] as usize;
        if len < 2 {
            // Impossible (must cover at least kind + checksum). Resync past this FF.
            malformed += 1;
            buf.drain(0..1);
            continue;
        }
        let total = 4 + len;
        if buf.len() < total {
            break; // wait for the rest
        }
        let sum = buf[2..total - 1]
            .iter()
            .fold(0u8, |acc, b| acc.wrapping_add(*b));
        if !sum != buf[total - 1] {
            crc += 1;
            buf.drain(0..1);
            continue;
        }
        let kind = buf[4];
        let params = buf[5..total - 1].to_vec();
        out.push(RawPacket { id, kind, params });
        buf.drain(0..total);
    }

    (out, crc, malformed)
}

/// Index of the first `FF FF` in `buf`, if any.
fn find_header(buf: &[u8]) -> Option<usize> {
    buf.windows(2).position(|w| w == [0xFF, 0xFF])
}

#[cfg(test)]
mod tests {
    use super::*;

    /// Build a full instruction packet (`FF FF id len instr params… cksum`) like the host would.
    fn instruction(id: u8, instr: u8, params: &[u8]) -> Vec<u8> {
        let len = (params.len() + 2) as u8;
        let mut body = vec![id, len, instr];
        body.extend_from_slice(params);
        let ck = checksum(&body);
        let mut pkt = vec![0xFF, 0xFF];
        pkt.extend_from_slice(&body);
        pkt.push(ck);
        pkt
    }

    /// Build a status (reply) packet (`FF FF id len err data… cksum`) like a servo would.
    fn status(id: u8, err: u8, data: &[u8]) -> Vec<u8> {
        let len = (data.len() + 2) as u8;
        let mut body = vec![id, len, err];
        body.extend_from_slice(data);
        let ck = checksum(&body);
        let mut pkt = vec![0xFF, 0xFF];
        pkt.extend_from_slice(&body);
        pkt.push(ck);
        pkt
    }

    fn read_req(id: u8, addr: u8, size: u8) -> Vec<u8> {
        instruction(id, INSTR_READ, &[addr, size])
    }

    fn sync_read_req(addr: u8, size: u8, ids: &[u8]) -> Vec<u8> {
        let mut params = vec![addr, size];
        params.extend_from_slice(ids);
        instruction(0xFE, INSTR_SYNC_READ, &params)
    }

    #[test]
    fn single_read_attribution() {
        let mut d = PortDecoder::new();
        // Host: READ(id=3, Present_Position, 2). No readings from the request itself.
        assert!(d
            .feed(Direction::HostToDevice, &read_req(3, PRESENT_POSITION, 2))
            .is_empty());
        // Device: status for id 3 carrying 0x0ABC.
        let r = d.feed(Direction::DeviceToHost, &status(3, 0, &[0xBC, 0x0A]));
        assert_eq!(
            r,
            vec![Reading {
                servo_id: 3,
                channel: Channel::Pos,
                raw: 0x0ABC
            }]
        );
    }

    #[test]
    fn read_of_other_register_is_not_position() {
        let mut d = PortDecoder::new();
        // A READ of some other register (e.g. Present_Voltage @ 62) must NOT become a pos reading.
        d.feed(Direction::HostToDevice, &read_req(1, 62, 1));
        let r = d.feed(Direction::DeviceToHost, &status(1, 0, &[120]));
        assert!(r.is_empty());
    }

    #[test]
    fn sync_read_attribution_all_six() {
        let mut d = PortDecoder::new();
        d.feed(
            Direction::HostToDevice,
            &sync_read_req(PRESENT_POSITION, 2, &[1, 2, 3, 4, 5, 6]),
        );
        // Six replies, one per servo, in id order.
        let mut got = Vec::new();
        for id in 1..=6u8 {
            let raw = 2000 + id as u16;
            got.extend(d.feed(
                Direction::DeviceToHost,
                &status(id, 0, &(raw).to_le_bytes()),
            ));
        }
        assert_eq!(got.len(), 6);
        for (i, r) in got.iter().enumerate() {
            let id = (i + 1) as u8;
            assert_eq!(r.servo_id, id);
            assert_eq!(r.channel, Channel::Pos);
            assert_eq!(r.raw, 2000 + id as u16);
        }
    }

    #[test]
    fn goal_position_write_extracted() {
        let mut d = PortDecoder::new();
        // WRITE(id=4, Goal_Position, 0x0640).
        let r = d.feed(
            Direction::HostToDevice,
            &instruction(4, INSTR_WRITE, &[GOAL_POSITION, 0x40, 0x06]),
        );
        assert_eq!(
            r,
            vec![Reading {
                servo_id: 4,
                channel: Channel::Cmd,
                raw: 0x0640
            }]
        );
    }

    #[test]
    fn sync_write_goal_position_extracted() {
        let mut d = PortDecoder::new();
        // SYNC_WRITE(Goal_Position, len=2, {1:100, 2:200, 3:300}).
        let mut params = vec![GOAL_POSITION, 2];
        for (id, val) in [(1u8, 100u16), (2, 200), (3, 300)] {
            params.push(id);
            params.extend_from_slice(&val.to_le_bytes());
        }
        let r = d.feed(
            Direction::HostToDevice,
            &instruction(0xFE, INSTR_SYNC_WRITE, &params),
        );
        assert_eq!(
            r,
            vec![
                Reading {
                    servo_id: 1,
                    channel: Channel::Cmd,
                    raw: 100
                },
                Reading {
                    servo_id: 2,
                    channel: Channel::Cmd,
                    raw: 200
                },
                Reading {
                    servo_id: 3,
                    channel: Channel::Cmd,
                    raw: 300
                },
            ]
        );
    }

    #[test]
    fn resync_after_leading_noise() {
        let mut d = PortDecoder::new();
        d.feed(Direction::HostToDevice, &read_req(2, PRESENT_POSITION, 2));
        // Garbage before a real status packet.
        let mut stream = vec![0x00, 0x13, 0xFF, 0x37, 0xAB];
        stream.extend_from_slice(&status(2, 0, &[0x11, 0x22]));
        let r = d.feed(Direction::DeviceToHost, &stream);
        assert_eq!(
            r,
            vec![Reading {
                servo_id: 2,
                channel: Channel::Pos,
                raw: 0x2211
            }]
        );
    }

    #[test]
    fn checksum_rejected() {
        let mut d = PortDecoder::new();
        d.feed(Direction::HostToDevice, &read_req(2, PRESENT_POSITION, 2));
        let mut bad = status(2, 0, &[0x11, 0x22]);
        let n = bad.len();
        bad[n - 1] ^= 0xFF; // corrupt checksum
        let r = d.feed(Direction::DeviceToHost, &bad);
        assert!(r.is_empty());
        assert_eq!(d.crc_failures(), 1);
    }

    #[test]
    fn partial_packet_spanning_feeds() {
        let mut d = PortDecoder::new();
        d.feed(Direction::HostToDevice, &read_req(5, PRESENT_POSITION, 2));
        let pkt = status(5, 0, &[0x34, 0x12]);
        let (a, b) = pkt.split_at(3); // header + id only
        assert!(d.feed(Direction::DeviceToHost, a).is_empty());
        let r = d.feed(Direction::DeviceToHost, b);
        assert_eq!(
            r,
            vec![Reading {
                servo_id: 5,
                channel: Channel::Pos,
                raw: 0x1234
            }]
        );
    }

    #[test]
    fn interleaved_both_directions() {
        // Two independent per-direction buffers: a request and a reply can be fed in any order
        // without corrupting each other.
        let mut d = PortDecoder::new();
        d.feed(
            Direction::HostToDevice,
            &sync_read_req(PRESENT_POSITION, 2, &[1, 2]),
        );
        let r1 = d.feed(
            Direction::DeviceToHost,
            &status(1, 0, &1500u16.to_le_bytes()),
        );
        // A new SYNC_WRITE (cmd) arrives interleaved before servo 2 replies.
        let mut params = vec![GOAL_POSITION, 2, 2u8];
        params.extend_from_slice(&2500u16.to_le_bytes());
        let rc = d.feed(
            Direction::HostToDevice,
            &instruction(0xFE, INSTR_SYNC_WRITE, &params),
        );
        assert_eq!(
            r1,
            vec![Reading {
                servo_id: 1,
                channel: Channel::Pos,
                raw: 1500
            }]
        );
        assert_eq!(
            rc,
            vec![Reading {
                servo_id: 2,
                channel: Channel::Cmd,
                raw: 2500
            }]
        );
    }

    #[test]
    fn sign_magnitude_decode() {
        assert_eq!(decode_sign_magnitude_15(0x0ABC), 0x0ABC);
        assert_eq!(decode_sign_magnitude_15(0x8005), -5);
        assert_eq!(decode_sign_magnitude_15(0x8000), 0);
    }
}
