//! Probe frame layout.
//!
//! ```text
//! 0      6      12     14       22       30     34     38
//! +------+------+------+--------+--------+------+------+---- padding ----+
//! | dst  | src  |88B5  | magic  | nonce  | step | seq  | zeros to size   |
//! +------+------+------+--------+--------+------+------+-----------------+
//! ```
//!
//! EtherType 0x88B5 is the IEEE 802 "local experimental" value, so probe
//! frames can never be mistaken for PPPoE (0x8863/0x8864) by a DUT. The run
//! nonce lets the receiver ignore frames from an earlier or concurrent run;
//! `step` separates the rate steps so that late frames are not counted in the
//! wrong step. All fields are big-endian.

/// IEEE 802 local experimental EtherType 1.
pub const ETHERTYPE: u16 = 0x88B5;
/// Payload magic, versioned.
pub const MAGIC: &[u8; 8] = b"BNGLAF01";
/// Ethernet header length.
pub const ETH_HLEN: usize = 14;
/// Probe header length after the Ethernet header.
pub const PROBE_HLEN: usize = 8 + 8 + 4 + 4;
/// Minimum frame size without FCS (IEEE 802.3 minimum of 64 minus 4).
pub const MIN_FRAME: usize = 60;
/// Maximum untagged frame size without FCS for a 1500-byte MTU.
pub const MAX_FRAME: usize = 1514;

const OFF_MAGIC: usize = ETH_HLEN;
const OFF_NONCE: usize = OFF_MAGIC + 8;
const OFF_STEP: usize = OFF_NONCE + 8;
const OFF_SEQ: usize = OFF_STEP + 4;

/// Builds a template frame of `size` bytes (clamped to 60..=1514) with the
/// Ethernet header, magic and nonce filled in. `step` and `seq` are written
/// per frame with [`stamp`].
pub fn build(dst: [u8; 6], src: [u8; 6], nonce: u64, size: usize) -> Vec<u8> {
    let size = size.clamp(MIN_FRAME, MAX_FRAME);
    let mut f = vec![0u8; size];
    f[0..6].copy_from_slice(&dst);
    f[6..12].copy_from_slice(&src);
    f[12..14].copy_from_slice(&ETHERTYPE.to_be_bytes());
    f[OFF_MAGIC..OFF_NONCE].copy_from_slice(MAGIC);
    f[OFF_NONCE..OFF_STEP].copy_from_slice(&nonce.to_be_bytes());
    f
}

/// Writes the step and sequence number into a frame built by [`build`].
#[inline]
pub fn stamp(frame: &mut [u8], step: u32, seq: u32) {
    frame[OFF_STEP..OFF_SEQ].copy_from_slice(&step.to_be_bytes());
    frame[OFF_SEQ..OFF_SEQ + 4].copy_from_slice(&seq.to_be_bytes());
}

/// Returns `(step, seq)` if `frame` is a probe frame of this run.
#[inline]
pub fn parse(frame: &[u8], nonce: u64) -> Option<(u32, u32)> {
    if frame.len() < ETH_HLEN + PROBE_HLEN
        || frame[12..14] != ETHERTYPE.to_be_bytes()
        || &frame[OFF_MAGIC..OFF_NONCE] != MAGIC
        || frame[OFF_NONCE..OFF_STEP] != nonce.to_be_bytes()
    {
        return None;
    }
    let step = u32::from_be_bytes(frame[OFF_STEP..OFF_SEQ].try_into().ok()?);
    let seq = u32::from_be_bytes(frame[OFF_SEQ..OFF_SEQ + 4].try_into().ok()?);
    Some((step, seq))
}

#[cfg(test)]
mod tests {
    use super::*;

    const A: [u8; 6] = [2, 0, 0, 0, 0, 1];
    const B: [u8; 6] = [2, 0, 0, 0, 0, 2];

    #[test]
    fn round_trip() {
        let mut f = build(B, A, 0xDEAD_BEEF, 64);
        assert_eq!(f.len(), 64);
        stamp(&mut f, 3, 4_000_000_000);
        assert_eq!(parse(&f, 0xDEAD_BEEF), Some((3, 4_000_000_000)));
        assert_eq!(&f[0..6], &B);
        assert_eq!(&f[6..12], &A);
    }

    #[test]
    fn rejects_foreign_frames() {
        let mut f = build(B, A, 1, 60);
        stamp(&mut f, 0, 1);
        assert_eq!(parse(&f, 2), None, "other run");
        assert_eq!(parse(&f[..30], 1), None, "short");
        f[12] = 0x88;
        f[13] = 0x63; // PPPoE discovery
        assert_eq!(parse(&f, 1), None, "other ethertype");
    }

    #[test]
    fn clamps_size() {
        assert_eq!(build(A, B, 0, 10).len(), MIN_FRAME);
        assert_eq!(build(A, B, 0, 9000).len(), MAX_FRAME);
    }
}
