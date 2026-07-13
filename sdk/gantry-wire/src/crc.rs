//! CRC-16/CCITT-FALSE (poly `0x1021`, init `0xFFFF`, no reflection, no final xor).

/// Fold one byte into a running CRC-16/CCITT-FALSE.
#[inline]
pub const fn update(mut crc: u16, byte: u8) -> u16 {
    crc ^= (byte as u16) << 8;
    let mut i = 0;
    while i < 8 {
        crc = if crc & 0x8000 != 0 {
            (crc << 1) ^ 0x1021
        } else {
            crc << 1
        };
        i += 1;
    }
    crc
}

/// CRC-16/CCITT-FALSE over a whole slice.
#[cfg(feature = "alloc")]
pub fn checksum(data: &[u8]) -> u16 {
    let mut crc = 0xFFFF;
    for &b in data {
        crc = update(crc, b);
    }
    crc
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn ccitt_false_check_vector() {
        // The canonical CRC-16/CCITT-FALSE check value for b"123456789" is 0x29B1.
        let mut crc = 0xFFFFu16;
        for &b in b"123456789" {
            crc = update(crc, b);
        }
        assert_eq!(crc, 0x29B1);
    }
}
