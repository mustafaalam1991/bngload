// Package netlinkx is a deliberately small rtnetlink client built on the
// standard library. It implements exactly the operations the Phase 0 kernel
// cost benchmark needs and that the Phase 5 virtual-network manager will
// reuse: create veth and macvlan links (optionally inside another network
// namespace), bring links up, add addresses and default routes, list and
// delete links.
//
// A Conn is bound to the network namespace that was current on the calling
// OS thread when Dial ran, which is how per-namespace configuration works
// without setns round trips for every message.
//
// Message format reference: rtnetlink(7), <linux/rtnetlink.h>,
// <linux/if_link.h>, <linux/veth.h>.
package netlinkx

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"os"
	"syscall"
	"unsafe"
)

// rtnetlink attribute and message constants not exported by package syscall.
const (
	iflaIfname   = 3
	iflaLink     = 5
	iflaLinkinfo = 18
	iflaNetNsFd  = 28

	iflaInfoKind = 1
	iflaInfoData = 2

	vethInfoPeer = 1

	iflaMacvlanMode   = 1
	MacvlanModeBridge = 4

	ifaLocal   = 2
	ifaAddress = 1

	rtaOif = 4

	rtTableMain   = 254
	rtProtoBoot   = 3
	rtScopeLink   = 253
	rtnUnicast    = 1
	sizeIfinfomsg = 16
	sizeIfaddrmsg = 8
	sizeRtmsg     = 12
)

// Conn is a NETLINK_ROUTE socket.
type Conn struct {
	fd  int
	seq uint32
}

// Dial opens an rtnetlink socket in the calling thread's network namespace.
func Dial() (*Conn, error) {
	fd, err := syscall.Socket(syscall.AF_NETLINK, syscall.SOCK_RAW|syscall.SOCK_CLOEXEC, syscall.NETLINK_ROUTE)
	if err != nil {
		return nil, fmt.Errorf("netlink socket: %w", err)
	}
	if err := syscall.Bind(fd, &syscall.SockaddrNetlink{Family: syscall.AF_NETLINK}); err != nil {
		syscall.Close(fd)
		return nil, fmt.Errorf("netlink bind: %w", err)
	}
	return &Conn{fd: fd}, nil
}

// Close releases the socket.
func (c *Conn) Close() error { return syscall.Close(c.fd) }

// ---------------------------------------------------------------------------
// Attribute encoding
// ---------------------------------------------------------------------------

// attr is an rtattr, optionally with nested children.
type attr struct {
	typ      uint16
	data     []byte
	children []attr
}

func align4(n int) int { return (n + 3) &^ 3 }

func (a attr) encode() []byte {
	body := a.data
	if len(a.children) > 0 {
		body = nil
		for _, ch := range a.children {
			body = append(body, ch.encode()...)
		}
	}
	l := 4 + len(body)
	b := make([]byte, align4(l))
	binary.NativeEndian.PutUint16(b[0:], uint16(l))
	binary.NativeEndian.PutUint16(b[2:], a.typ)
	copy(b[4:], body)
	return b
}

func strAttr(t uint16, s string) attr { return attr{typ: t, data: append([]byte(s), 0)} }

func u32Attr(t uint16, v uint32) attr {
	b := make([]byte, 4)
	binary.NativeEndian.PutUint32(b, v)
	return attr{typ: t, data: b}
}

func encodeAttrs(as ...attr) []byte {
	var b []byte
	for _, a := range as {
		b = append(b, a.encode()...)
	}
	return b
}

// ifinfomsg builds struct ifinfomsg.
func ifinfomsg(family uint8, index int32, flags, change uint32) []byte {
	b := make([]byte, sizeIfinfomsg)
	b[0] = family
	binary.NativeEndian.PutUint32(b[4:], uint32(index))
	binary.NativeEndian.PutUint32(b[8:], flags)
	binary.NativeEndian.PutUint32(b[12:], change)
	return b
}

// ---------------------------------------------------------------------------
// Request / response
// ---------------------------------------------------------------------------

// request sends one message and reads replies until DONE (dumps) or the
// ACK/error for this sequence number.
func (c *Conn) request(typ, flags uint16, payload []byte) ([]syscall.NetlinkMessage, error) {
	c.seq++
	l := syscall.NLMSG_HDRLEN + len(payload)
	b := make([]byte, align4(l))
	binary.NativeEndian.PutUint32(b[0:], uint32(l))
	binary.NativeEndian.PutUint16(b[4:], typ)
	binary.NativeEndian.PutUint16(b[6:], flags|syscall.NLM_F_REQUEST|syscall.NLM_F_ACK)
	binary.NativeEndian.PutUint32(b[8:], c.seq)
	copy(b[syscall.NLMSG_HDRLEN:], payload)
	if err := syscall.Sendto(c.fd, b, 0, &syscall.SockaddrNetlink{Family: syscall.AF_NETLINK}); err != nil {
		return nil, fmt.Errorf("netlink send: %w", err)
	}
	var out []syscall.NetlinkMessage
	for {
		// A fresh buffer per datagram: parsed messages alias the buffer, so
		// reusing it would let the trailing ACK overwrite earlier replies.
		rb := make([]byte, 1<<16)
		n, _, err := syscall.Recvfrom(c.fd, rb, 0)
		if err != nil {
			return nil, fmt.Errorf("netlink recv: %w", err)
		}
		msgs, err := syscall.ParseNetlinkMessage(rb[:n])
		if err != nil {
			return nil, fmt.Errorf("netlink parse: %w", err)
		}
		for _, m := range msgs {
			if m.Header.Seq != c.seq {
				continue
			}
			switch m.Header.Type {
			case syscall.NLMSG_DONE:
				return out, nil
			case syscall.NLMSG_ERROR:
				if len(m.Data) < 4 {
					return nil, errors.New("netlink: short error message")
				}
				if code := int32(binary.NativeEndian.Uint32(m.Data[0:4])); code != 0 {
					return nil, syscall.Errno(-code)
				}
				return out, nil // ACK
			default:
				out = append(out, m)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Links
// ---------------------------------------------------------------------------

// Link is a minimal view of a network interface.
type Link struct {
	Index int
	Name  string
}

// Links dumps all links in the connection's namespace.
func (c *Conn) Links() ([]Link, error) {
	msgs, err := c.request(syscall.RTM_GETLINK, syscall.NLM_F_DUMP, ifinfomsg(syscall.AF_UNSPEC, 0, 0, 0))
	if err != nil {
		return nil, err
	}
	var out []Link
	for _, m := range msgs {
		if m.Header.Type != syscall.RTM_NEWLINK || len(m.Data) < sizeIfinfomsg {
			continue
		}
		l := Link{Index: int(int32(binary.NativeEndian.Uint32(m.Data[4:8])))}
		attrs, err := syscall.ParseNetlinkRouteAttr(&m)
		if err != nil {
			continue
		}
		for _, a := range attrs {
			if a.Attr.Type == iflaIfname {
				l.Name = string(trimNul(a.Value))
			}
		}
		out = append(out, l)
	}
	return out, nil
}

// LinkIndex returns the index of the named link, or an error wrapping
// os.ErrNotExist.
func (c *Conn) LinkIndex(name string) (int, error) {
	msgs, err := c.request(syscall.RTM_GETLINK, 0,
		append(ifinfomsg(syscall.AF_UNSPEC, 0, 0, 0), encodeAttrs(strAttr(iflaIfname, name))...))
	if err != nil {
		if errors.Is(err, syscall.ENODEV) {
			return 0, fmt.Errorf("link %s: %w", name, os.ErrNotExist)
		}
		return 0, err
	}
	for _, m := range msgs {
		if m.Header.Type == syscall.RTM_NEWLINK && len(m.Data) >= sizeIfinfomsg {
			return int(int32(binary.NativeEndian.Uint32(m.Data[4:8]))), nil
		}
	}
	return 0, fmt.Errorf("link %s: %w", name, os.ErrNotExist)
}

// SetUp brings a link administratively up.
func (c *Conn) SetUp(index int) error {
	_, err := c.request(syscall.RTM_NEWLINK, 0,
		ifinfomsg(syscall.AF_UNSPEC, int32(index), syscall.IFF_UP, syscall.IFF_UP))
	return err
}

// Delete removes a link.
func (c *Conn) Delete(index int) error {
	_, err := c.request(syscall.RTM_DELLINK, 0, ifinfomsg(syscall.AF_UNSPEC, int32(index), 0, 0))
	return err
}

// AddVeth creates a veth pair. When peerNsFd >= 0 the peer end is created
// directly inside that network namespace (no move needed).
func (c *Conn) AddVeth(name, peer string, peerNsFd int) error {
	peerAttrs := []attr{strAttr(iflaIfname, peer)}
	if peerNsFd >= 0 {
		peerAttrs = append(peerAttrs, u32Attr(iflaNetNsFd, uint32(peerNsFd)))
	}
	peerInfo := append(ifinfomsg(syscall.AF_UNSPEC, 0, 0, 0), encodeAttrs(peerAttrs...)...)
	// IFLA_INFO_KIND is sent without a trailing NUL, as iproute2 does.
	linkinfo := attr{typ: iflaLinkinfo, children: []attr{
		{typ: iflaInfoKind, data: []byte("veth")},
		{typ: iflaInfoData, children: []attr{{typ: vethInfoPeer, data: peerInfo}}},
	}}
	payload := append(ifinfomsg(syscall.AF_UNSPEC, 0, 0, 0), encodeAttrs(strAttr(iflaIfname, name), linkinfo)...)
	_, err := c.request(syscall.RTM_NEWLINK, syscall.NLM_F_CREATE|syscall.NLM_F_EXCL, payload)
	return err
}

// AddMacvlan creates a macvlan on parent. When nsFd >= 0 the new link is
// created inside that network namespace.
func (c *Conn) AddMacvlan(name string, parent int, nsFd int, mode uint32) error {
	attrs := []attr{
		strAttr(iflaIfname, name),
		u32Attr(iflaLink, uint32(parent)),
		{typ: iflaLinkinfo, children: []attr{
			{typ: iflaInfoKind, data: []byte("macvlan")},
			{typ: iflaInfoData, children: []attr{u32Attr(iflaMacvlanMode, mode)}},
		}},
	}
	if nsFd >= 0 {
		attrs = append(attrs, u32Attr(iflaNetNsFd, uint32(nsFd)))
	}
	payload := append(ifinfomsg(syscall.AF_UNSPEC, 0, 0, 0), encodeAttrs(attrs...)...)
	_, err := c.request(syscall.RTM_NEWLINK, syscall.NLM_F_CREATE|syscall.NLM_F_EXCL, payload)
	return err
}

// ---------------------------------------------------------------------------
// Addresses and routes
// ---------------------------------------------------------------------------

// AddAddr assigns ip/prefixLen to a link.
func (c *Conn) AddAddr(index int, ip net.IP, prefixLen int) error {
	family := uint8(syscall.AF_INET6)
	raw := ip.To16()
	if v4 := ip.To4(); v4 != nil {
		family, raw = syscall.AF_INET, v4
	}
	hdr := make([]byte, sizeIfaddrmsg)
	hdr[0] = family
	hdr[1] = uint8(prefixLen)
	binary.NativeEndian.PutUint32(hdr[4:], uint32(index))
	payload := append(hdr, encodeAttrs(attr{typ: ifaLocal, data: raw}, attr{typ: ifaAddress, data: raw})...)
	_, err := c.request(syscall.RTM_NEWADDR, syscall.NLM_F_CREATE|syscall.NLM_F_EXCL, payload)
	return err
}

// AddDefaultRouteDev installs a default route out of a point-to-point link
// (no gateway), as used for a per-session TUN device.
func (c *Conn) AddDefaultRouteDev(index int, ipv6 bool) error {
	hdr := make([]byte, sizeRtmsg)
	hdr[0] = syscall.AF_INET
	if ipv6 {
		hdr[0] = syscall.AF_INET6
	}
	hdr[4] = rtTableMain
	hdr[5] = rtProtoBoot
	hdr[6] = rtScopeLink
	hdr[7] = rtnUnicast
	payload := append(hdr, encodeAttrs(u32Attr(rtaOif, uint32(index)))...)
	_, err := c.request(syscall.RTM_NEWROUTE, syscall.NLM_F_CREATE|syscall.NLM_F_EXCL, payload)
	return err
}

func trimNul(b []byte) []byte {
	for i, c := range b {
		if c == 0 {
			return b[:i]
		}
	}
	return b
}

// compile-time check that the ifinfomsg size matches the kernel ABI.
var _ = [1]struct{}{}[unsafe.Sizeof(syscall.IfInfomsg{})-sizeIfinfomsg]
