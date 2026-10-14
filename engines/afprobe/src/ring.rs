//! `TPACKET_V3` memory-mapped RX ring (see the kernel's
//! `Documentation/networking/packet_mmap.rst`).
//!
//! The ring is a sequence of fixed-size *blocks*. The kernel fills a block
//! with variable-length frames and hands it to user space by setting
//! `TP_STATUS_USER` in the block header, either when the block is full or
//! when the retire timeout (`tp_retire_blk_tov`, milliseconds) expires. User
//! space walks the frames and returns the block by writing `TP_STATUS_KERNEL`.
//! There is no copy and one `poll` wakes the reader for a whole block, which
//! is why the design picks TPACKET_V3 as the default engine (design 2.2).
//!
//! The retire timeout delays the *reader*, not the kernel timestamps in the
//! frame headers (design MR-6), so afprobe uses 1 ms by default.

use crate::sys::{self, PacketSocket, TpacketStatsV3};
use std::io;
use std::mem::size_of;
use std::os::fd::{OwnedFd, RawFd};
use std::ptr::{addr_of, addr_of_mut, read_volatile, write_volatile};
use std::sync::atomic::{fence, Ordering};

const TP_STATUS_KERNEL: u32 = 0;
const TP_STATUS_USER: u32 = 1;
/// Frame slot size handed to the kernel; V3 packs frames tighter, but the
/// value must be a multiple of 16 and divide the block size.
const FRAME_SLOT: usize = 2048;

/// `struct tpacket_req3`.
#[repr(C)]
#[derive(Debug, Clone, Copy)]
struct TpacketReq3 {
    tp_block_size: u32,
    tp_block_nr: u32,
    tp_frame_size: u32,
    tp_frame_nr: u32,
    tp_retire_blk_tov: u32,
    tp_sizeof_priv: u32,
    tp_feature_req_word: u32,
}

/// `struct tpacket_bd_ts`.
#[repr(C)]
#[derive(Debug, Clone, Copy)]
struct BdTs {
    ts_sec: u32,
    ts_nsec: u32,
}

/// `struct tpacket_hdr_v1`.
#[repr(C)]
#[derive(Debug, Clone, Copy)]
struct HdrV1 {
    block_status: u32,
    num_pkts: u32,
    offset_to_first_pkt: u32,
    blk_len: u32,
    seq_num: u64,
    ts_first_pkt: BdTs,
    ts_last_pkt: BdTs,
}

/// `struct tpacket_block_desc` (the header union only has `bh1`).
#[repr(C)]
#[derive(Debug, Clone, Copy)]
struct BlockDesc {
    version: u32,
    offset_to_priv: u32,
    hdr: HdrV1,
}

/// `struct tpacket3_hdr`.
#[repr(C)]
#[derive(Debug, Clone, Copy)]
struct Tpacket3Hdr {
    tp_next_offset: u32,
    tp_sec: u32,
    tp_nsec: u32,
    tp_snaplen: u32,
    tp_len: u32,
    tp_status: u32,
    tp_mac: u16,
    tp_net: u16,
    tp_rxhash: u32,
    tp_vlan_tci: u32,
    tp_vlan_tpid: u16,
    tp_padding_hv1: u16,
    tp_padding: [u8; 8],
}

/// Ring geometry.
#[derive(Debug, Clone, Copy)]
pub struct RingConfig {
    /// Block size in bytes; a power of two and a multiple of the page size.
    pub block_size: usize,
    /// Number of blocks.
    pub block_nr: usize,
    /// Block retire timeout in milliseconds.
    pub retire_ms: u32,
}

impl Default for RingConfig {
    fn default() -> Self {
        Self { block_size: 1 << 20, block_nr: 64, retire_ms: 1 }
    }
}

impl RingConfig {
    /// Checks the constraints the kernel enforces, with readable errors.
    pub fn validate(&self) -> io::Result<()> {
        let page = 4096;
        let bad = |m: String| Err(io::Error::new(io::ErrorKind::InvalidInput, m));
        if !self.block_size.is_power_of_two() || self.block_size < page {
            return bad(format!("ring block size {} must be a power of two ≥ {page}", self.block_size));
        }
        if self.block_nr == 0 || self.block_nr > 65_536 {
            return bad(format!("ring block count {} must be 1..=65536", self.block_nr));
        }
        if self.retire_ms == 0 {
            return bad("block retire timeout must be ≥ 1 ms".into());
        }
        Ok(())
    }

    pub fn bytes(&self) -> usize {
        self.block_size * self.block_nr
    }
}

/// A TPACKET_V3 receive ring bound to one interface and EtherType.
pub struct RxRing {
    sock: PacketSocket,
    map: *mut u8,
    cfg: RingConfig,
    cur: usize,
}

// SAFETY: the mapping is owned by this value and only accessed through
// `&mut self`; moving it to another thread is sound.
unsafe impl Send for RxRing {}

impl RxRing {
    /// Creates the socket, configures the ring and maps it.
    pub fn open(ifindex: u32, ethertype: u16, cfg: RingConfig) -> io::Result<Self> {
        cfg.validate()?;
        let sock = PacketSocket::open(ifindex, ethertype)?;
        sock.set_opt(sys::SOL_PACKET, sys::PACKET_VERSION, &sys::TPACKET_V3)?;
        // Ask for precise software RX stamps (skb->tstamp). Without it the
        // ring may fall back to a coarse clock on some kernels.
        let on: libc::c_int = 1;
        let _ = sock.set_opt(libc::SOL_SOCKET, sys::SO_TIMESTAMPNS_OLD, &on);
        let req = TpacketReq3 {
            tp_block_size: cfg.block_size as u32,
            tp_block_nr: cfg.block_nr as u32,
            tp_frame_size: FRAME_SLOT as u32,
            tp_frame_nr: (cfg.block_size / FRAME_SLOT * cfg.block_nr) as u32,
            tp_retire_blk_tov: cfg.retire_ms,
            tp_sizeof_priv: 0,
            tp_feature_req_word: 0,
        };
        sock.set_opt(sys::SOL_PACKET, sys::PACKET_RX_RING, &req)?;
        // SAFETY: mapping the ring of the socket we own with the size we
        // configured; checked for MAP_FAILED below.
        let map = unsafe {
            libc::mmap(
                std::ptr::null_mut(),
                cfg.bytes(),
                libc::PROT_READ | libc::PROT_WRITE,
                libc::MAP_SHARED | libc::MAP_POPULATE,
                sock.raw(),
                0,
            )
        };
        if map == libc::MAP_FAILED {
            return Err(io::Error::last_os_error());
        }
        Ok(Self { sock, map: map as *mut u8, cfg, cur: 0 })
    }

    /// Descriptor duplicate for reading `PACKET_STATISTICS` from elsewhere.
    pub fn stats_fd(&self) -> io::Result<OwnedFd> {
        self.sock.try_clone_fd()
    }

    pub fn raw_fd(&self) -> RawFd {
        self.sock.raw()
    }

    /// Reads and resets the ring statistics.
    pub fn stats(&self) -> io::Result<TpacketStatsV3> {
        sys::packet_stats(self.sock.raw())
    }

    fn block(&self, i: usize) -> *mut BlockDesc {
        // SAFETY: i < block_nr, so the offset stays inside the mapping.
        unsafe { self.map.add(i * self.cfg.block_size) as *mut BlockDesc }
    }

    /// Hands every ready block to `f` (frame bytes, kernel RX time in ns),
    /// waiting up to `timeout_ms` when none is ready. At most one full lap of
    /// the ring is processed per call so the caller regains control
    /// regularly. Returns the number of frames processed.
    pub fn drain<F: FnMut(&[u8], u64)>(&mut self, timeout_ms: i32, mut f: F) -> io::Result<usize> {
        let mut frames = 0usize;
        for lap in 0..self.cfg.block_nr {
            let bd = self.block(self.cur);
            // SAFETY: `bd` points at a block header inside the mapping; the
            // kernel writes it concurrently, hence the volatile read.
            let mut status = unsafe { read_volatile(addr_of!((*bd).hdr.block_status)) };
            if status & TP_STATUS_USER == 0 {
                if lap > 0 || !sys::poll_in(self.sock.raw(), timeout_ms)? {
                    break;
                }
                // SAFETY: as above.
                status = unsafe { read_volatile(addr_of!((*bd).hdr.block_status)) };
                if status & TP_STATUS_USER == 0 {
                    break;
                }
            }
            // Pair with the kernel's smp_wmb before it publishes the block.
            fence(Ordering::Acquire);
            // SAFETY: the block belongs to user space until released below.
            let (num, first) = unsafe { ((*bd).hdr.num_pkts, (*bd).hdr.offset_to_first_pkt as usize) };
            let base = bd as *const u8;
            let mut off = first;
            for _ in 0..num {
                if off + size_of::<Tpacket3Hdr>() > self.cfg.block_size {
                    break;
                }
                // SAFETY: bounds checked against the block size above.
                let ph = unsafe { &*(base.add(off) as *const Tpacket3Hdr) };
                let start = off + ph.tp_mac as usize;
                let len = ph.tp_snaplen as usize;
                if start + len <= self.cfg.block_size {
                    // SAFETY: the frame lies inside this block (checked).
                    let data = unsafe { std::slice::from_raw_parts(base.add(start), len) };
                    f(data, ph.tp_sec as u64 * 1_000_000_000 + ph.tp_nsec as u64);
                }
                frames += 1;
                if ph.tp_next_offset == 0 {
                    break;
                }
                off += ph.tp_next_offset as usize;
            }
            fence(Ordering::Release);
            // SAFETY: returning the block to the kernel.
            unsafe { write_volatile(addr_of_mut!((*bd).hdr.block_status), TP_STATUS_KERNEL) };
            self.cur = (self.cur + 1) % self.cfg.block_nr;
        }
        Ok(frames)
    }
}

impl Drop for RxRing {
    fn drop(&mut self) {
        // SAFETY: unmapping the region mapped in `open`, exactly once.
        unsafe { libc::munmap(self.map as *mut libc::c_void, self.cfg.bytes()) };
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    /// The structures must match the kernel ABI (x86_64 and arm64 agree).
    #[test]
    fn abi_sizes() {
        assert_eq!(size_of::<TpacketReq3>(), 28);
        assert_eq!(size_of::<HdrV1>(), 40);
        assert_eq!(size_of::<BlockDesc>(), 48);
        assert_eq!(size_of::<Tpacket3Hdr>(), 48);
        assert_eq!(std::mem::offset_of!(BlockDesc, hdr), 8);
        assert_eq!(std::mem::offset_of!(Tpacket3Hdr, tp_mac), 24);
    }

    #[test]
    fn config_validation() {
        assert!(RingConfig::default().validate().is_ok());
        assert!(RingConfig { block_size: 3000, ..Default::default() }.validate().is_err());
        assert!(RingConfig { block_nr: 0, ..Default::default() }.validate().is_err());
        assert!(RingConfig { retire_ms: 0, ..Default::default() }.validate().is_err());
    }
}
