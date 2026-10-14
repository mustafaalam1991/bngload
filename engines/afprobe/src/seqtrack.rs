//! Per-step receive accounting: unique frames, duplicates, reordering and the
//! kernel timestamps of the first and last frame (for the RX rate).

/// Sequence numbers above this are counted but not checked for duplicates,
/// which bounds the bitmap at 16 MiB per step.
pub const MAX_TRACKED_SEQ: u32 = 1 << 27;

/// Receive accounting for one rate step.
#[derive(Debug, Default, Clone)]
pub struct SeqTrack {
    /// Unique probe frames received.
    pub received: u64,
    /// Frames whose sequence number had already been seen.
    pub duplicates: u64,
    /// Frames that arrived after a higher sequence number.
    pub out_of_order: u64,
    /// Frames beyond [`MAX_TRACKED_SEQ`] (counted, not de-duplicated).
    pub untracked: u64,
    /// Kernel RX timestamp (ns) of the first and last frame.
    pub first_ts_ns: u64,
    pub last_ts_ns: u64,
    max_seq: Option<u32>,
    bits: Vec<u64>,
}

impl SeqTrack {
    /// Records one received frame.
    #[inline]
    pub fn record(&mut self, seq: u32, ts_ns: u64) {
        if seq < MAX_TRACKED_SEQ {
            let w = (seq / 64) as usize;
            if w >= self.bits.len() {
                let grow = (w + 1).max(self.bits.len() * 2).min((MAX_TRACKED_SEQ / 64) as usize);
                self.bits.resize(grow, 0);
            }
            let bit = 1u64 << (seq % 64);
            if self.bits[w] & bit != 0 {
                self.duplicates += 1;
                return;
            }
            self.bits[w] |= bit;
        } else {
            self.untracked += 1;
        }
        self.received += 1;
        match self.max_seq {
            Some(m) if seq < m => self.out_of_order += 1,
            _ => self.max_seq = Some(seq),
        }
        if self.first_ts_ns == 0 {
            self.first_ts_ns = ts_ns;
        }
        self.last_ts_ns = ts_ns;
    }

    /// Pre-sizes the bitmap for `frames` expected frames.
    pub fn reserve(&mut self, frames: u64) {
        let words = (frames.min(MAX_TRACKED_SEQ as u64) / 64 + 1) as usize;
        if words > self.bits.len() {
            self.bits.resize(words, 0);
        }
    }

    /// Receive rate from kernel timestamps, if at least two frames arrived.
    pub fn rate_pps(&self) -> Option<f64> {
        if self.received < 2 || self.last_ts_ns <= self.first_ts_ns {
            return None;
        }
        Some((self.received - 1) as f64 / ((self.last_ts_ns - self.first_ts_ns) as f64 / 1e9))
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn counts_duplicates_and_reordering() {
        let mut t = SeqTrack::default();
        for (s, ts) in [(0, 1_000), (1, 2_000), (3, 3_000), (2, 4_000), (2, 5_000), (200, 6_000)] {
            t.record(s, ts);
        }
        assert_eq!(t.received, 5);
        assert_eq!(t.duplicates, 1);
        assert_eq!(t.out_of_order, 1);
        assert_eq!((t.first_ts_ns, t.last_ts_ns), (1_000, 6_000));
    }

    #[test]
    fn rate_from_timestamps() {
        let mut t = SeqTrack::default();
        for i in 0..1001u32 {
            t.record(i, 1_000_000_000 + i as u64 * 1_000); // 1 µs apart → 1 Mpps
        }
        let r = t.rate_pps().unwrap();
        assert!((r - 1e6).abs() < 1.0, "{r}");
        assert_eq!(SeqTrack::default().rate_pps(), None);
    }

    #[test]
    fn untracked_sequence_numbers() {
        let mut t = SeqTrack::default();
        t.record(MAX_TRACKED_SEQ + 5, 1);
        t.record(MAX_TRACKED_SEQ + 5, 2);
        assert_eq!((t.received, t.untracked, t.duplicates), (2, 2, 0));
    }
}
