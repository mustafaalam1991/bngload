// Package afpacket provides a minimal AF_PACKET (raw Ethernet) socket with
// kernel and NIC timestamping, built only on the standard library.
//
// Phase 0 uses it for the timestamp self-test. Phase 1 (PPPoE discovery
// prototype, design section 15) starts from the same socket: plain
// AF_PACKET send/receive with SO_TIMESTAMPING, before the TPACKET_V3 ring
// engine replaces it in Phase 2/6.
//
// Timestamps (Linux Documentation/networking/timestamping.rst):
//
//   - RX stamps arrive as an SCM_TIMESTAMPING control message on recvmsg.
//   - TX stamps arrive on the socket error queue (recvmsg with MSG_ERRQUEUE)
//     after the driver hands the frame to the NIC (software stamp) and/or
//     after the NIC reports transmit completion (hardware stamp).
//   - SCM_TIMESTAMPING carries three timespecs: [0] software stamp
//     (CLOCK_REALTIME), [1] legacy (unused), [2] raw hardware stamp (NIC PHC).
package afpacket

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"syscall"
	"time"
	"unsafe"

	"github.com/mustafaalam1991/bngload/internal/linux"
)

// EthertypeLocalExperimental1 is the IEEE Std 802 "Local Experimental
// Ethertype 1". Self-test frames use it so they can never be mistaken for
// PPPoE (0x8863/0x8864), IP or any production protocol.
const EthertypeLocalExperimental1 = 0x88B5

// packetQdiscBypass is PACKET_QDISC_BYPASS (SOL_PACKET), missing from
// package syscall.
const packetQdiscBypass = 20

// ErrTimeout is returned when no frame or timestamp arrives in time.
var ErrTimeout = errors.New("timeout")

// Socket is an AF_PACKET SOCK_RAW socket bound to one interface and one
// Ethertype.
type Socket struct {
	fd        int
	ifindex   int
	ethertype uint16
	Name      string
	MAC       net.HardwareAddr
}

// Open creates a socket on ifname that receives only frames of ethertype.
// Requires CAP_NET_RAW.
func Open(ifname string, ethertype uint16) (*Socket, error) {
	ifi, err := net.InterfaceByName(ifname)
	if err != nil {
		return nil, fmt.Errorf("interface %s: %w", ifname, err)
	}
	proto := linux.Htons(ethertype)
	fd, err := syscall.Socket(syscall.AF_PACKET, syscall.SOCK_RAW|syscall.SOCK_CLOEXEC, int(proto))
	if err != nil {
		return nil, fmt.Errorf("AF_PACKET socket (needs CAP_NET_RAW): %w", err)
	}
	sa := &syscall.SockaddrLinklayer{Protocol: proto, Ifindex: ifi.Index}
	if err := syscall.Bind(fd, sa); err != nil {
		syscall.Close(fd)
		return nil, fmt.Errorf("bind AF_PACKET to %s: %w", ifname, err)
	}
	return &Socket{fd: fd, ifindex: ifi.Index, ethertype: ethertype, Name: ifname, MAC: ifi.HardwareAddr}, nil
}

// Fd returns the file descriptor (for polling).
func (s *Socket) Fd() int { return s.fd }

// Close releases the socket.
func (s *Socket) Close() error { return syscall.Close(s.fd) }

// EnableTimestamping sets SO_TIMESTAMPING with the given SOF_TIMESTAMPING_*
// flags (see package nic for the flag constants).
func (s *Socket) EnableTimestamping(flags uint32) error {
	if err := syscall.SetsockoptInt(s.fd, syscall.SOL_SOCKET, syscall.SO_TIMESTAMPING, int(flags)); err != nil {
		return fmt.Errorf("SO_TIMESTAMPING on %s: %w", s.Name, err)
	}
	return nil
}

// SetQdiscBypass sends frames straight to the driver, skipping the qdisc
// layer, as the design recommends for TX sockets (section 2.2).
func (s *Socket) SetQdiscBypass(on bool) error {
	v := 0
	if on {
		v = 1
	}
	return syscall.SetsockoptInt(s.fd, syscall.SOL_PACKET, packetQdiscBypass, v)
}

// Send transmits a complete Ethernet frame (header included).
func (s *Socket) Send(frame []byte) error {
	if len(frame) < 14 {
		return errors.New("frame shorter than an Ethernet header")
	}
	var addr [8]byte
	copy(addr[:], frame[0:6])
	sa := &syscall.SockaddrLinklayer{Protocol: linux.Htons(s.ethertype), Ifindex: s.ifindex, Halen: 6, Addr: addr}
	return syscall.Sendto(s.fd, frame, 0, sa)
}

// Stamps holds the timestamps carried by one SCM_TIMESTAMPING message, in
// nanoseconds since the epoch of the respective clock. Zero means "absent".
type Stamps struct {
	Software int64 // CLOCK_REALTIME
	Hardware int64 // NIC PHC
}

// ParseStamps extracts SCM_TIMESTAMPING from control-message bytes.
func ParseStamps(oob []byte) (Stamps, bool) {
	msgs, err := syscall.ParseSocketControlMessage(oob)
	if err != nil {
		return Stamps{}, false
	}
	for _, m := range msgs {
		if m.Header.Level != syscall.SOL_SOCKET || m.Header.Type != syscall.SCM_TIMESTAMPING {
			continue
		}
		// struct scm_timestamping { struct timespec ts[3]; }
		const tsz = int(unsafe.Sizeof(syscall.Timespec{}))
		if len(m.Data) < 3*tsz {
			return Stamps{}, false
		}
		return Stamps{
			Software: timespecNs(m.Data[0:tsz]),
			Hardware: timespecNs(m.Data[2*tsz : 3*tsz]),
		}, true
	}
	return Stamps{}, false
}

func timespecNs(b []byte) int64 {
	sec := int64(binary.NativeEndian.Uint64(b[0:8]))
	nsec := int64(binary.NativeEndian.Uint64(b[8:16]))
	return sec*1e9 + nsec
}

// Recv waits up to timeout for one frame and returns its length and RX
// stamps. ErrTimeout is returned when nothing arrives.
func (s *Socket) Recv(buf, oob []byte, timeout time.Duration) (int, Stamps, error) {
	ready, err := linux.Poll([]linux.PollFd{{Fd: int32(s.fd), Events: linux.POLLIN}}, timeout)
	if err != nil {
		return 0, Stamps{}, err
	}
	if ready == 0 {
		return 0, Stamps{}, ErrTimeout
	}
	n, oobn, _, _, err := syscall.Recvmsg(s.fd, buf, oob, syscall.MSG_DONTWAIT)
	if err != nil {
		if err == syscall.EAGAIN {
			return 0, Stamps{}, ErrTimeout
		}
		return 0, Stamps{}, err
	}
	st, _ := ParseStamps(oob[:oobn])
	return n, st, nil
}

// RecvTxStamps waits up to timeout for the error queue to become readable and
// then drains it, returning every TX timestamp message found. With both
// software and hardware stamping enabled a single frame typically yields two
// messages (driver hand-off, then NIC completion).
func (s *Socket) RecvTxStamps(timeout time.Duration) ([]Stamps, error) {
	// POLLERR is always reported; asking for no events waits only for it.
	ready, err := linux.Poll([]linux.PollFd{{Fd: int32(s.fd), Events: 0}}, timeout)
	if err != nil {
		return nil, err
	}
	if ready == 0 {
		return nil, ErrTimeout
	}
	var out []Stamps
	buf := make([]byte, 256)
	oob := make([]byte, 512)
	for {
		_, oobn, _, _, err := syscall.Recvmsg(s.fd, buf, oob, syscall.MSG_ERRQUEUE|syscall.MSG_DONTWAIT)
		if err != nil {
			if err == syscall.EAGAIN {
				break
			}
			return out, err
		}
		if st, ok := ParseStamps(oob[:oobn]); ok {
			out = append(out, st)
		}
	}
	if len(out) == 0 {
		return nil, ErrTimeout
	}
	return out, nil
}

// Stats are the socket counters from PACKET_STATISTICS. Reading them resets
// the kernel counters.
type Stats struct {
	Packets uint32 `json:"packets"`
	Drops   uint32 `json:"drops"`
}

// Statistics reads and resets PACKET_STATISTICS.
func (s *Socket) Statistics() (Stats, error) {
	var st Stats // struct tpacket_stats { u32 tp_packets; u32 tp_drops; }
	l := uint32(unsafe.Sizeof(st))
	_, _, errno := syscall.Syscall6(syscall.SYS_GETSOCKOPT, uintptr(s.fd), syscall.SOL_PACKET,
		syscall.PACKET_STATISTICS, uintptr(unsafe.Pointer(&st)), uintptr(unsafe.Pointer(&l)), 0)
	if errno != 0 {
		return Stats{}, errno
	}
	return st, nil
}

// BuildFrame assembles an Ethernet II frame and pads it to minLen bytes
// (60 is the Ethernet minimum without FCS).
func BuildFrame(dst, src net.HardwareAddr, ethertype uint16, payload []byte, minLen int) []byte {
	n := 14 + len(payload)
	if n < minLen {
		n = minLen
	}
	f := make([]byte, n)
	copy(f[0:6], dst)
	copy(f[6:12], src)
	binary.BigEndian.PutUint16(f[12:14], ethertype)
	copy(f[14:], payload)
	return f
}
