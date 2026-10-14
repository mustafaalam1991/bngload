// Package linux wraps the handful of Linux system calls and kernel ABI
// structures that the Go standard library does not expose: ioctl with an
// ifreq data pointer, ppoll, setns, clock_gettime on arbitrary clock IDs
// (including PTP hardware clocks) and capability inspection.
//
// Everything here is a thin, documented translation of a kernel interface.
// Higher-level packages (nic, clock, tstamp, kbench) build on it. The raw
// syscalls live here; other kernel-interface packages (nic, pktio/afpacket,
// kbench, netlinkx) use unsafe only to pass ABI structs to these wrappers or
// to check struct sizes, which keeps it easy to audit (design SR-25).
package linux

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

// ---------------------------------------------------------------------------
// ioctl
// ---------------------------------------------------------------------------

// Ioctl request numbers used by bngload (from <linux/sockios.h>).
const (
	SIOCETHTOOL   = 0x8946 // ethtool command (struct ifreq with ifr_data)
	SIOCGHWTSTAMP = 0x89b1 // get hardware timestamping config
	SIOCSHWTSTAMP = 0x89b0 // set hardware timestamping config
)

// ifreqData mirrors struct ifreq when the union member is a pointer
// (ifr_data). struct ifreq is 40 bytes on 64-bit Linux: a 16-byte name and a
// 24-byte union. The pointer is typed unsafe.Pointer so the Go garbage
// collector keeps the referenced buffer alive for the duration of the call.
type ifreqData struct {
	name [syscall.IFNAMSIZ]byte
	data unsafe.Pointer
	_    [24 - unsafe.Sizeof(uintptr(0))]byte
}

// IoctlIfreqData issues an ioctl whose argument is a struct ifreq carrying a
// pointer to data, on a throw-away AF_INET datagram socket. This is how
// ethtool(8) and hwstamp_ctl(8) talk to network drivers.
func IoctlIfreqData(ifname string, req uintptr, data unsafe.Pointer) error {
	if len(ifname) >= syscall.IFNAMSIZ {
		return fmt.Errorf("interface name %q too long", ifname)
	}
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_DGRAM|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("socket for ioctl: %w", err)
	}
	defer syscall.Close(fd)
	var ifr ifreqData
	copy(ifr.name[:], ifname)
	ifr.data = data
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), req, uintptr(unsafe.Pointer(&ifr)))
	if errno != 0 {
		return errno
	}
	return nil
}

// Ioctl issues ioctl(fd, req, arg) where arg points to caller memory.
func Ioctl(fd int, req uintptr, arg unsafe.Pointer) error {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), req, uintptr(arg))
	if errno != 0 {
		return errno
	}
	return nil
}

// ---------------------------------------------------------------------------
// ppoll
// ---------------------------------------------------------------------------

// Poll event bits (from <poll.h>).
const (
	POLLIN  = 0x1
	POLLPRI = 0x2
	POLLOUT = 0x4
	POLLERR = 0x8
)

// PollFd mirrors struct pollfd.
type PollFd struct {
	Fd      int32
	Events  int16
	Revents int16
}

// Poll waits until one of fds is ready or the timeout expires. It uses the
// ppoll system call because plain poll(2) does not exist on arm64. A negative
// timeout blocks indefinitely. EINTR is reported as zero ready descriptors.
func Poll(fds []PollFd, timeout time.Duration) (int, error) {
	if len(fds) == 0 {
		return 0, nil
	}
	var tsp *syscall.Timespec
	if timeout >= 0 {
		ts := syscall.NsecToTimespec(timeout.Nanoseconds())
		tsp = &ts
	}
	n, _, errno := syscall.Syscall6(syscall.SYS_PPOLL,
		uintptr(unsafe.Pointer(&fds[0])), uintptr(len(fds)),
		uintptr(unsafe.Pointer(tsp)), 0, 0, 0)
	if errno == syscall.EINTR {
		return 0, nil
	}
	if errno != 0 {
		return 0, errno
	}
	return int(n), nil
}

// ---------------------------------------------------------------------------
// setns
// ---------------------------------------------------------------------------

// Setns moves the calling OS thread into the namespace referenced by fd.
// nstype is a CLONE_NEW* constant (or 0 for "any"). The caller must have
// locked the goroutine to its thread with runtime.LockOSThread, otherwise the
// Go scheduler may run other goroutines in the wrong namespace (design 2.1,
// "namespace operations run on dedicated locked threads").
func Setns(fd int, nstype int) error {
	_, _, errno := syscall.RawSyscall(sysSetns, uintptr(fd), uintptr(nstype), 0)
	if errno != 0 {
		return errno
	}
	return nil
}

// ---------------------------------------------------------------------------
// clock_gettime
// ---------------------------------------------------------------------------

// Clock IDs (from <linux/time.h>).
const (
	ClockRealtime     = 0
	ClockMonotonic    = 1
	ClockMonotonicRaw = 4
	ClockTAI          = 11
)

// ClockGettime reads clockid and returns nanoseconds since that clock's
// epoch. It issues the system call directly (no vDSO), which costs roughly a
// microsecond: acceptable for the sampling done in Phase 0, never used on a
// packet hot path.
func ClockGettime(clockid int) (int64, error) {
	var ts syscall.Timespec
	_, _, errno := syscall.Syscall(syscall.SYS_CLOCK_GETTIME, uintptr(clockid), uintptr(unsafe.Pointer(&ts)), 0)
	if errno != 0 {
		return 0, errno
	}
	return ts.Nano(), nil
}

// FDToClockID converts an open /dev/ptpN file descriptor into a dynamic
// POSIX clock ID usable with clock_gettime (kernel FD_TO_CLOCKID macro).
func FDToClockID(fd int) int {
	return int((^uint32(fd) << 3) | 3)
}

// ---------------------------------------------------------------------------
// Capabilities
// ---------------------------------------------------------------------------

// Capability numbers (from <linux/capability.h>) relevant to bngload.
var CapNames = map[int]string{
	12: "CAP_NET_ADMIN",
	13: "CAP_NET_RAW",
	14: "CAP_IPC_LOCK",
	21: "CAP_SYS_ADMIN",
	23: "CAP_SYS_NICE",
	25: "CAP_SYS_TIME",
	38: "CAP_PERFMON",
	39: "CAP_BPF",
}

// EffectiveCaps returns the names of the effective capabilities of this
// process that appear in CapNames, read from /proc/self/status.
func EffectiveCaps() (map[string]bool, error) {
	return effectiveCapsFrom("/proc/self/status")
}

func effectiveCapsFrom(path string) (map[string]bool, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	for _, line := range strings.Split(string(b), "\n") {
		if !strings.HasPrefix(line, "CapEff:") {
			continue
		}
		mask, err := strconv.ParseUint(strings.TrimSpace(strings.TrimPrefix(line, "CapEff:")), 16, 64)
		if err != nil {
			return nil, fmt.Errorf("parse CapEff: %w", err)
		}
		out := map[string]bool{}
		for bit, name := range CapNames {
			out[name] = mask&(1<<uint(bit)) != 0
		}
		return out, nil
	}
	return nil, errors.New("CapEff not found in " + path)
}

// Htons converts a 16-bit value to network byte order.
func Htons(v uint16) uint16 { return v<<8 | v>>8 }
