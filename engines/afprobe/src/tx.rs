//! Paced `sendmmsg` transmitter.
//!
//! Frames are sent in batches. With a target rate, batch *k* is due at
//! `start + k·B/R`; the sender sleeps until shortly before the due time and
//! spins for the rest, then records the *schedule lag* (actual − due). That
//! lag is the I/O-path share of the design's schedule-lag metric (PR-5) and of
//! the 2.1 exit criterion ("schedule lag p99 < 1 ms").
//!
//! The batch size is reduced at low rates so a batch never spans more than
//! about 100 µs of schedule: at 50 kpps the sender emits 5-frame bursts, at
//! 1 Mpps the full batch (32 by default).

use crate::frame;
use crate::sys::{self, PacketSocket};
use std::io;
use std::sync::atomic::{AtomicBool, Ordering};

/// What to send in one step.
#[derive(Debug, Clone)]
pub struct TxPlan {
    /// Target frames per second; `None` sends as fast as possible.
    pub rate: Option<u64>,
    pub duration_ns: u64,
    /// Maximum frames per `sendmmsg` call.
    pub max_batch: usize,
    pub step: u32,
}

/// Outcome of one step.
#[derive(Debug, Clone, Default)]
pub struct TxResult {
    /// Frames handed to `sendmmsg`.
    pub attempted: u64,
    /// Frames the kernel accepted.
    pub sent: u64,
    /// Frames refused with ENOBUFS/EAGAIN (driver queue full with
    /// qdisc bypass, or socket buffer exhausted).
    pub refused: u64,
    /// Frames refused with any other error.
    pub errors: u64,
    pub last_error: Option<String>,
    /// Wall time from the first to the end of the last send.
    pub wall_ns: u64,
    /// CPU time of the sending thread (includes the pacing spin).
    pub cpu_ns: u64,
    /// Time spent inside `sendmmsg`, i.e. the send cost without pacing
    /// waits. `sent / send_ns` is the per-core TX capacity.
    pub send_ns: u64,
    /// Schedule lag per batch, ns (paced runs only).
    pub lag_ns: Vec<f64>,
    /// Batch size actually used.
    pub batch: usize,
    /// True when the step ended early (signal or persistent errors).
    pub aborted: bool,
}

/// Batch size that keeps each burst within ~100 µs of schedule.
pub fn effective_batch(rate: Option<u64>, max_batch: usize) -> usize {
    let max_batch = max_batch.max(1);
    match rate {
        None => max_batch,
        Some(r) => ((r / 10_000) as usize).clamp(1, max_batch),
    }
}

/// Due time of the frame with index `frames` at `rate` frames/s.
#[inline]
pub fn due_ns(start_ns: u64, frames: u64, rate: u64) -> u64 {
    start_ns + (frames as u128 * 1_000_000_000u128 / rate.max(1) as u128) as u64
}

/// Sleeps until 100 µs before `due`, then spins.
#[inline]
fn wait_until(due: u64) {
    let now = sys::mono_ns();
    if due > now + 200_000 {
        sys::sleep_until_mono(due - 100_000);
    }
    while sys::mono_ns() < due {
        std::hint::spin_loop();
    }
}

/// Runs one step on `sock`, sending copies of `template` (built by
/// [`frame::build`]) with per-frame step and sequence numbers.
pub fn run(sock: &PacketSocket, template: &[u8], plan: &TxPlan, stop: &AtomicBool) -> TxResult {
    let b = effective_batch(plan.rate, plan.max_batch);
    let mut res = TxResult { batch: b, ..Default::default() };

    // Batch buffers and the iovec/mmsghdr arrays pointing at them. The
    // buffers are never reallocated after this point.
    let mut bufs: Vec<Vec<u8>> = (0..b).map(|_| template.to_vec()).collect();
    let mut iov: Vec<libc::iovec> = bufs
        .iter_mut()
        .map(|f| libc::iovec { iov_base: f.as_mut_ptr() as *mut libc::c_void, iov_len: f.len() })
        .collect();
    let mut msgs: Vec<libc::mmsghdr> = iov
        .iter_mut()
        .map(|v| {
            // SAFETY: mmsghdr is plain old data; zero is a valid initial value.
            let mut m: libc::mmsghdr = unsafe { std::mem::zeroed() };
            m.msg_hdr.msg_iov = v as *mut libc::iovec;
            m.msg_hdr.msg_iovlen = 1;
            m
        })
        .collect();

    if let Some(r) = plan.rate {
        let batches = (r as u128 * plan.duration_ns as u128 / 1_000_000_000 / b as u128) as usize;
        res.lag_ns.reserve(batches.min(4_000_000) + 16);
    }

    let cpu0 = sys::thread_cpu_ns();
    let start = sys::mono_ns();
    let end = start + plan.duration_ns;
    let mut last = start;
    let mut seq: u64 = 0;
    let mut consecutive_errors = 0u32;

    'outer: loop {
        if stop.load(Ordering::Relaxed) || sys::STOP.load(Ordering::Relaxed) {
            res.aborted = true;
            break;
        }
        match plan.rate {
            Some(r) => {
                let due = due_ns(start, seq, r);
                if due >= end {
                    break;
                }
                wait_until(due);
                res.lag_ns.push((sys::mono_ns() - due) as f64);
            }
            None => {
                if sys::mono_ns() >= end {
                    break;
                }
            }
        }
        for (i, v) in iov.iter().enumerate() {
            // Write through the iovec's pointer (the same provenance the
            // kernel reads through), not through a fresh borrow of `bufs`.
            // SAFETY: iov_base/iov_len describe a live buffer in `bufs` that
            // nothing else accesses while this slice exists.
            let frame = unsafe { std::slice::from_raw_parts_mut(v.iov_base as *mut u8, v.iov_len) };
            frame::stamp(frame, plan.step, (seq + i as u64) as u32);
        }
        let mut off = 0usize;
        let t_send = sys::mono_ns();
        while off < b {
            // SAFETY: msgs[off..b] are valid mmsghdrs whose iovecs point at
            // live buffers of the stated length.
            let n = unsafe { libc::sendmmsg(sock.raw(), msgs[off..].as_mut_ptr(), (b - off) as libc::c_uint, 0) };
            if n > 0 {
                res.sent += n as u64;
                off += n as usize;
                consecutive_errors = 0;
                continue;
            }
            let e = io::Error::last_os_error();
            match e.raw_os_error() {
                Some(libc::EINTR) => continue,
                Some(libc::ENOBUFS) | Some(libc::EAGAIN) => res.refused += 1,
                _ => {
                    res.errors += 1;
                    res.last_error = Some(e.to_string());
                }
            }
            off += 1; // the failed frame is skipped, not retried
            consecutive_errors += 1;
            if consecutive_errors > 100_000 {
                res.aborted = true;
                res.attempted += off as u64;
                break 'outer;
            }
        }
        res.attempted += b as u64;
        seq += b as u64;
        last = sys::mono_ns();
        res.send_ns += last - t_send;
    }
    res.wall_ns = last - start;
    res.cpu_ns = sys::thread_cpu_ns() - cpu0;
    // Keep the buffers alive until all sends are done.
    drop(msgs);
    drop(iov);
    drop(bufs);
    res
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn batch_shrinks_at_low_rates() {
        assert_eq!(effective_batch(Some(5_000), 32), 1);
        assert_eq!(effective_batch(Some(50_000), 32), 5);
        assert_eq!(effective_batch(Some(2_000_000), 32), 32);
        assert_eq!(effective_batch(None, 64), 64);
        assert_eq!(effective_batch(None, 0), 1);
    }

    #[test]
    fn due_times_are_exact() {
        assert_eq!(due_ns(100, 0, 1_000), 100);
        assert_eq!(due_ns(100, 1_000, 1_000), 1_000_000_100);
        assert_eq!(due_ns(0, 3, 3_000_000), 1_000);
        // no overflow at large frame counts
        assert_eq!(due_ns(0, u32::MAX as u64 * 4, 1), u32::MAX as u64 * 4 * 1_000_000_000);
    }
}
