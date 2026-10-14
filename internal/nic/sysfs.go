package nic

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// SysfsRoot is the sysfs mount point. Tests point it at a fixture tree.
var SysfsRoot = "/sys"

// ProcRoot is the procfs mount point. Tests point it at a fixture tree.
var ProcRoot = "/proc"

// Link holds sysfs and address facts about one interface.
type Link struct {
	Name        string   `json:"name"`
	Index       int      `json:"index"`
	MAC         string   `json:"mac"`
	MTU         int      `json:"mtu"`
	OperState   string   `json:"operstate"`
	Carrier     bool     `json:"carrier"`
	SpeedMbps   int      `json:"speed_mbps"`
	Promiscuous bool     `json:"promiscuous"`
	Virtual     bool     `json:"virtual"`
	PCIAddress  string   `json:"pci_address,omitempty"`
	NUMANode    int      `json:"numa_node"`
	RxQueues    int      `json:"rx_queues"`
	TxQueues    int      `json:"tx_queues"`
	IPv4        []string `json:"ipv4"`
	IPv6Global  []string `json:"ipv6_global"`
	IPv6Link    []string `json:"ipv6_link_local"`
	// IPv6Disabled reflects net.ipv6.conf.<if>.disable_ipv6.
	IPv6Disabled bool `json:"ipv6_disabled"`
}

// iffPromisc is IFF_PROMISC from <linux/if.h>.
const iffPromisc = 0x100

// LinkFacts collects sysfs attributes and addresses for ifname.
func LinkFacts(ifname string) (Link, error) {
	ifi, err := net.InterfaceByName(ifname)
	if err != nil {
		return Link{}, fmt.Errorf("interface %s: %w", ifname, err)
	}
	base := filepath.Join(SysfsRoot, "class/net", ifname)
	l := Link{
		Name:      ifname,
		Index:     ifi.Index,
		MAC:       ifi.HardwareAddr.String(),
		MTU:       ifi.MTU,
		OperState: readString(filepath.Join(base, "operstate")),
		Carrier:   readString(filepath.Join(base, "carrier")) == "1",
		SpeedMbps: readInt(filepath.Join(base, "speed"), -1),
		NUMANode:  readInt(filepath.Join(base, "device/numa_node"), -1),
	}
	if flags, err := strconv.ParseUint(strings.TrimPrefix(readString(filepath.Join(base, "flags")), "0x"), 16, 64); err == nil {
		l.Promiscuous = flags&iffPromisc != 0
	}
	if dev, err := os.Readlink(filepath.Join(base, "device")); err == nil {
		l.PCIAddress = filepath.Base(dev)
	} else {
		l.Virtual = true
	}
	l.RxQueues = countPrefix(filepath.Join(base, "queues"), "rx-")
	l.TxQueues = countPrefix(filepath.Join(base, "queues"), "tx-")
	l.IPv6Disabled = readString(filepath.Join(ProcRoot, "sys/net/ipv6/conf", ifname, "disable_ipv6")) == "1"

	addrs, err := ifi.Addrs()
	if err != nil {
		return l, fmt.Errorf("addresses of %s: %w", ifname, err)
	}
	for _, a := range addrs {
		ipn, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		switch {
		case ipn.IP.To4() != nil:
			l.IPv4 = append(l.IPv4, ipn.String())
		case ipn.IP.IsLinkLocalUnicast():
			l.IPv6Link = append(l.IPv6Link, ipn.String())
		default:
			l.IPv6Global = append(l.IPv6Global, ipn.String())
		}
	}
	return l, nil
}

// HasRoutableAddress reports an IPv4 or non-link-local IPv6 address. The
// design (SR-19, 2.2) requires the test interface to carry none.
func (l Link) HasRoutableAddress() bool {
	return len(l.IPv4) > 0 || len(l.IPv6Global) > 0
}

func readString(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func readInt(path string, def int) int {
	v, err := strconv.Atoi(readString(path))
	if err != nil {
		return def
	}
	return v
}

func countPrefix(dir, prefix string) int {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), prefix) {
			n++
		}
	}
	return n
}
