//! Thin wrappers around the Linux system calls afprobe needs.
//!
//! Every `unsafe` block here is a single FFI call with arguments built from
//! owned, correctly sized values. Kernel ABI constants are defined locally
//! (with their uapi header names) instead of taken from `libc`, so the probe
//! does not depend on which `libc` release added them.

use std::ffi::{CStr, CString};
use std::io;
use std::mem::{size_of, zeroed};
use std::net::{IpAddr, Ipv4Addr, Ipv6Addr};
use std::os::fd::{AsRawFd, FromRawFd, OwnedFd, RawFd};
use std::path::Path;
use std::sync::atomic::{AtomicBool, Ordering};

// <linux/socket.h>, <linux/if_packet.h>
pub const SOL_PACKET: libc::c_int = 263;
pub const PACKET_RX_RING: libc::c_int = 5;
pub const PACKET_STATISTICS: libc::c_int = 6;
pub const PACKET_VERSION: libc::c_int = 10;
pub const PACKET_QDISC_BYPASS: libc::c_int = 20;
pub const TPACKET_V3: libc::c_int = 2;
// <asm-generic/socket.h>
pub const SO_TIMESTAMPNS_OLD: libc::c_int = 35;

/// `struct tpacket_stats_v3`. Reading it resets the kernel counters.
#[repr(C)]
#[derive(Debug, Default, Clone, Copy, PartialEq, Eq)]
pub struct TpacketStatsV3 {
    pub tp_packets: u32,
    pub tp_drops: u32,
    pub tp_freeze_q_cnt: u32,
}

fn invalid(msg: impl Into<String>) -> io::Error {
    io::Error::new(io::ErrorKind::InvalidInput, msg.into())
}

/// Interface index for a name.
pub fn if_index(name: &str) -> io::Result<u32> {
    let c = CString::new(name).map_err(|_| invalid("interface name contains NUL"))?;
    // SAFETY: `c` is a valid NUL-terminated string for the duration of the call.
    let idx = unsafe { libc::if_nametoindex(c.as_ptr()) };
    if idx == 0 {
        Err(io::Error::new(io::ErrorKind::NotFound, format!("interface {name} not found")))
    } else {
        Ok(idx)
    }
}

/// Reads a sysfs/procfs attribute, trimmed.
pub fn read_attr(path: impl AsRef<Path>) -> Option<String> {
    std::fs::read_to_string(path).ok().map(|s| s.trim().to_string())
}

/// Parses `aa:bb:cc:dd:ee:ff`.
pub fn parse_mac(s: &str) -> Option<[u8; 6]> {
    let mut out = [0u8; 6];
    let mut parts = s.trim().split(':');
    for b in out.iter_mut() {
        *b = u8::from_str_radix(parts.next()?, 16).ok()?;
    }
    parts.next().is_none().then_some(out)
}

/// Formats a MAC address.
pub fn fmt_mac(m: &[u8; 6]) -> String {
    format!("{:02x}:{:02x}:{:02x}:{:02x}:{:02x}:{:02x}", m[0], m[1], m[2], m[3], m[4], m[5])
}

/// MAC address of an interface (from sysfs).
pub fn mac_of(ifname: &str) -> io::Result<[u8; 6]> {
    read_attr(format!("/sys/class/net/{ifname}/address"))
        .as_deref()
        .and_then(parse_mac)
        .ok_or_else(|| io::Error::new(io::ErrorKind::NotFound, format!("no MAC address for {ifname}")))
}

/// True when the interface has no backing device (veth, bridge, dummy…).
pub fn is_virtual(ifname: &str) -> bool {
    !Path::new(&format!("/sys/class/net/{ifname}/device")).exists()
}

/// Kernel driver bound to the interface's device, if any.
pub fn driver_of(ifname: &str) -> Option<String> {
    let p = std::fs::read_link(format!("/sys/class/net/{ifname}/device/driver")).ok()?;
    p.file_name().map(|n| n.to_string_lossy().into_owned())
}

/// A counter from `/sys/class/net/<if>/statistics/`.
pub fn nic_counter(ifname: &str, counter: &str) -> Option<u64> {
    read_attr(format!("/sys/class/net/{ifname}/statistics/{counter}"))?.parse().ok()
}

/// Aggregate CPU times from the first line of `/proc/stat`, in clock ticks.
#[derive(Debug, Clone, Copy, Default, PartialEq, Eq)]
pub struct CpuTimes {
    pub total: u64,
    /// Everything except idle and iowait.
    pub busy: u64,
    /// Hard and soft interrupt time (where NIC RX and veth delivery run).
    pub irq: u64,
}

/// Parses the aggregate `cpu` line of `/proc/stat`.
pub fn parse_proc_stat(text: &str) -> Option<CpuTimes> {
    let line = text.lines().find(|l| l.starts_with("cpu "))?;
    let v: Vec<u64> = line.split_whitespace().skip(1).filter_map(|x| x.parse().ok()).collect();
    if v.len() < 7 {
        return None;
    }
    // user nice system idle iowait irq softirq steal guest guest_nice;
    // guest time is already included in user/nice.
    let total: u64 = v.iter().take(8).sum();
    Some(CpuTimes { total, busy: total - v[3] - v[4], irq: v[5] + v[6] })
}

/// Current aggregate CPU times.
pub fn cpu_times() -> Option<CpuTimes> {
    parse_proc_stat(&std::fs::read_to_string("/proc/stat").ok()?)
}

/// Number of online CPUs.
pub fn online_cpus() -> usize {
    std::thread::available_parallelism().map(|n| n.get()).unwrap_or(1)
}

/// An `AF_PACKET` raw socket bound to one interface.
#[derive(Debug)]
pub struct PacketSocket {
    fd: OwnedFd,
}

impl PacketSocket {
    /// Opens a socket bound to `ifindex`. `protocol` is the host-order
    /// EtherType to receive; 0 receives nothing (a pure TX socket).
    pub fn open(ifindex: u32, protocol: u16) -> io::Result<Self> {
        let proto_be = protocol.to_be();
        // SAFETY: plain socket(2) call.
        let raw =
            unsafe { libc::socket(libc::AF_PACKET, libc::SOCK_RAW | libc::SOCK_CLOEXEC, proto_be as libc::c_int) };
        if raw < 0 {
            return Err(io::Error::last_os_error());
        }
        // SAFETY: `raw` is a freshly created descriptor we exclusively own.
        let fd = unsafe { OwnedFd::from_raw_fd(raw) };
        // SAFETY: sockaddr_ll is plain old data; all-zero is a valid value.
        let mut sll: libc::sockaddr_ll = unsafe { zeroed() };
        sll.sll_family = libc::AF_PACKET as u16;
        sll.sll_protocol = proto_be;
        sll.sll_ifindex = ifindex as i32;
        // SAFETY: pointer and length describe `sll`.
        let r = unsafe {
            libc::bind(
                fd.as_raw_fd(),
                &sll as *const libc::sockaddr_ll as *const libc::sockaddr,
                size_of::<libc::sockaddr_ll>() as libc::socklen_t,
            )
        };
        if r < 0 {
            return Err(io::Error::last_os_error());
        }
        Ok(Self { fd })
    }

    pub fn raw(&self) -> RawFd {
        self.fd.as_raw_fd()
    }

    /// Duplicates the descriptor (used to read statistics from another thread).
    pub fn try_clone_fd(&self) -> io::Result<OwnedFd> {
        self.fd.try_clone()
    }

    /// setsockopt with a value of type `T`.
    pub fn set_opt<T: Copy>(&self, level: libc::c_int, name: libc::c_int, value: &T) -> io::Result<()> {
        set_opt(self.raw(), level, name, value)
    }
}

/// setsockopt on a raw descriptor.
pub fn set_opt<T: Copy>(fd: RawFd, level: libc::c_int, name: libc::c_int, value: &T) -> io::Result<()> {
    // SAFETY: pointer and length describe `value`.
    let r = unsafe {
        libc::setsockopt(fd, level, name, value as *const T as *const libc::c_void, size_of::<T>() as libc::socklen_t)
    };
    if r < 0 {
        Err(io::Error::last_os_error())
    } else {
        Ok(())
    }
}

/// Reads (and thereby resets) `PACKET_STATISTICS` of a TPACKET_V3 socket.
pub fn packet_stats(fd: RawFd) -> io::Result<TpacketStatsV3> {
    let mut st = TpacketStatsV3::default();
    let mut len = size_of::<TpacketStatsV3>() as libc::socklen_t;
    // SAFETY: pointer and length describe `st`.
    let r = unsafe {
        libc::getsockopt(fd, SOL_PACKET, PACKET_STATISTICS, &mut st as *mut _ as *mut libc::c_void, &mut len)
    };
    if r < 0 {
        Err(io::Error::last_os_error())
    } else {
        Ok(st)
    }
}

/// Waits until `fd` is readable or `timeout_ms` passes. Returns true when
/// readable; EINTR is reported as "not readable".
pub fn poll_in(fd: RawFd, timeout_ms: i32) -> io::Result<bool> {
    let mut p = libc::pollfd { fd, events: libc::POLLIN | libc::POLLERR, revents: 0 };
    // SAFETY: one valid pollfd.
    let r = unsafe { libc::poll(&mut p, 1, timeout_ms) };
    if r < 0 {
        let e = io::Error::last_os_error();
        return if e.kind() == io::ErrorKind::Interrupted { Ok(false) } else { Err(e) };
    }
    Ok(r > 0)
}

fn clock_ns(id: libc::clockid_t) -> u64 {
    // SAFETY: timespec is plain old data.
    let mut ts: libc::timespec = unsafe { zeroed() };
    // SAFETY: valid clock id and out-pointer.
    unsafe { libc::clock_gettime(id, &mut ts) };
    ts.tv_sec as u64 * 1_000_000_000 + ts.tv_nsec as u64
}

/// CLOCK_MONOTONIC in nanoseconds.
#[inline]
pub fn mono_ns() -> u64 {
    clock_ns(libc::CLOCK_MONOTONIC)
}

/// CPU time consumed by the calling thread, in nanoseconds.
#[inline]
pub fn thread_cpu_ns() -> u64 {
    clock_ns(libc::CLOCK_THREAD_CPUTIME_ID)
}

/// Sleeps until CLOCK_MONOTONIC reaches `deadline_ns`.
pub fn sleep_until_mono(deadline_ns: u64) {
    let ts = libc::timespec {
        tv_sec: (deadline_ns / 1_000_000_000) as libc::time_t,
        tv_nsec: (deadline_ns % 1_000_000_000) as libc::c_long,
    };
    loop {
        // SAFETY: valid timespec; remaining time is not needed with TIMER_ABSTIME.
        let r = unsafe { libc::clock_nanosleep(libc::CLOCK_MONOTONIC, libc::TIMER_ABSTIME, &ts, std::ptr::null_mut()) };
        if r != libc::EINTR {
            break;
        }
    }
}

/// Pins the calling thread to one CPU.
pub fn pin_current_thread(cpu: usize) -> io::Result<()> {
    // SAFETY: cpu_set_t is a bitmask; zeroed is the empty set.
    let mut set: libc::cpu_set_t = unsafe { zeroed() };
    if cpu >= 8 * size_of::<libc::cpu_set_t>() {
        return Err(invalid(format!("cpu {cpu} out of range")));
    }
    // SAFETY: index checked above.
    unsafe { libc::CPU_SET(cpu, &mut set) };
    // SAFETY: pid 0 is the calling thread; pointer and size describe `set`.
    let r = unsafe { libc::sched_setaffinity(0, size_of::<libc::cpu_set_t>(), &set) };
    if r < 0 {
        Err(io::Error::last_os_error())
    } else {
        Ok(())
    }
}

/// 64 random bits from the kernel.
pub fn random_u64() -> u64 {
    let mut b = [0u8; 8];
    // SAFETY: buffer and length describe `b`.
    let n = unsafe { libc::getrandom(b.as_mut_ptr() as *mut libc::c_void, 8, 0) };
    if n == 8 {
        u64::from_ne_bytes(b)
    } else {
        mono_ns() ^ (std::process::id() as u64) << 32
    }
}

/// (hostname, kernel release) from uname(2).
pub fn uname() -> (String, String) {
    // SAFETY: utsname is plain old data.
    let mut u: libc::utsname = unsafe { zeroed() };
    // SAFETY: valid out-pointer.
    if unsafe { libc::uname(&mut u) } != 0 {
        return (String::new(), String::new());
    }
    // SAFETY: the kernel NUL-terminates both fields.
    let f = |a: &[libc::c_char]| unsafe { CStr::from_ptr(a.as_ptr()) }.to_string_lossy().into_owned();
    (f(&u.nodename), f(&u.release))
}

/// All IP addresses configured on `ifname` (getifaddrs(3)).
pub fn addresses_of(ifname: &str) -> io::Result<Vec<IpAddr>> {
    let mut head: *mut libc::ifaddrs = std::ptr::null_mut();
    // SAFETY: valid out-pointer; freed below.
    if unsafe { libc::getifaddrs(&mut head) } != 0 {
        return Err(io::Error::last_os_error());
    }
    let mut out = Vec::new();
    let mut p = head;
    while !p.is_null() {
        // SAFETY: `p` walks the list returned by getifaddrs until NULL.
        let ifa = unsafe { &*p };
        // SAFETY: ifa_name is a NUL-terminated string owned by the list.
        let name = unsafe { CStr::from_ptr(ifa.ifa_name) };
        if name.to_bytes() == ifname.as_bytes() && !ifa.ifa_addr.is_null() {
            // SAFETY: ifa_addr is non-null and points at a sockaddr whose
            // family selects the concrete type.
            unsafe {
                match (*ifa.ifa_addr).sa_family as libc::c_int {
                    libc::AF_INET => {
                        let sin = &*(ifa.ifa_addr as *const libc::sockaddr_in);
                        out.push(IpAddr::V4(Ipv4Addr::from(u32::from_be(sin.sin_addr.s_addr))));
                    }
                    libc::AF_INET6 => {
                        let sin6 = &*(ifa.ifa_addr as *const libc::sockaddr_in6);
                        out.push(IpAddr::V6(Ipv6Addr::from(sin6.sin6_addr.s6_addr)));
                    }
                    _ => {}
                }
            }
        }
        p = ifa.ifa_next;
    }
    // SAFETY: `head` came from getifaddrs and is freed exactly once.
    unsafe { libc::freeifaddrs(head) };
    Ok(out)
}

/// Set by SIGINT/SIGTERM; checked by every loop.
pub static STOP: AtomicBool = AtomicBool::new(false);

extern "C" fn on_signal(_: libc::c_int) {
    STOP.store(true, Ordering::Relaxed);
}

/// Installs SIGINT/SIGTERM handlers that request a clean stop.
pub fn install_stop_handlers() {
    let h = on_signal as extern "C" fn(libc::c_int) as libc::sighandler_t;
    // SAFETY: the handler only stores to an atomic (async-signal-safe).
    unsafe {
        libc::signal(libc::SIGINT, h);
        libc::signal(libc::SIGTERM, h);
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn mac_round_trip() {
        let m = parse_mac("02:aB:00:10:ff:01").unwrap();
        assert_eq!(fmt_mac(&m), "02:ab:00:10:ff:01");
        assert!(parse_mac("02:ab:00:10:ff").is_none());
        assert!(parse_mac("02:ab:00:10:ff:01:02").is_none());
        assert!(parse_mac("zz:ab:00:10:ff:01").is_none());
    }

    #[test]
    fn proc_stat_parsing() {
        let t = parse_proc_stat("cpu  100 0 50 800 10 5 15 0 0 0\ncpu0 1 2 3 4 5 6 7 8 9 10\n").unwrap();
        assert_eq!(t, CpuTimes { total: 980, busy: 170, irq: 20 });
        assert!(parse_proc_stat("intr 1 2 3").is_none());
        assert!(cpu_times().is_some_and(|c| c.total > 0));
    }

    #[test]
    fn stats_struct_matches_kernel_abi() {
        assert_eq!(size_of::<TpacketStatsV3>(), 12);
    }

    #[test]
    fn clocks_advance() {
        let a = mono_ns();
        sleep_until_mono(a + 2_000_000);
        assert!(mono_ns() - a >= 2_000_000);
        let c = thread_cpu_ns();
        let mut x = 0u64;
        for i in 0..1_000_000u64 {
            x = x.wrapping_add(i * i);
        }
        std::hint::black_box(x);
        assert!(thread_cpu_ns() > c);
    }

    #[test]
    fn loopback_lookups() {
        if let Ok(idx) = if_index("lo") {
            assert!(idx > 0);
            assert!(is_virtual("lo"));
            let addrs = addresses_of("lo").unwrap();
            assert!(addrs.iter().all(|a| a.is_loopback()));
        }
        assert!(if_index("bkb-no-such-if").is_err());
    }
}
