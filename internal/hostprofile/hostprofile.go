// Package hostprofile builds the generator host profile
// (`bngload host profile`), the Phase 0 deliverable "host profile with NIC
// timestamp support verified" (design section 15).
//
// A host profile is a JSON document committed to hosts/<name>.json. It pins
// down every hardware and OS fact that can change a benchmark result, so a
// result can later be reproduced or explained (design FR-161). Measured
// capacity figures (afprobe I/O ceiling, kbench costs, later the TS-16
// calibration ceiling) are merged into the "measured" section by the Phase 0
// acceptance report.
package hostprofile

import (
	"sort"

	"github.com/mustafaalam1991/bngload/internal/clock"
	"github.com/mustafaalam1991/bngload/internal/nic"
	"github.com/mustafaalam1991/bngload/internal/safety"
	"github.com/mustafaalam1991/bngload/internal/sysinfo"
)

// interestingFeatures are the offloads recorded per interface.
// Names are the kernel feature strings (netdev_features_strings), which
// differ from some ethtool -k display names: "rx-vlan-hw-parse" is shown as
// rx-vlan-offload, "rx-gro" as generic-receive-offload.
var interestingFeatures = []string{
	"rx-vlan-hw-parse", "rx-vlan-stag-hw-parse", "tx-vlan-hw-insert", "tx-vlan-stag-hw-insert",
	"rx-checksum", "rx-gro", "rx-lro", "rx-hashing", "rx-ntuple-filter",
}

// Interface is everything recorded about one test port.
type Interface struct {
	Link         nic.Link            `json:"link"`
	Driver       *nic.DriverInfo     `json:"driver,omitempty"`
	Timestamping *nic.TsInfo         `json:"timestamping,omitempty"`
	HwTstamp     *nic.HwTstampConfig `json:"hw_tstamp_config,omitempty"`
	Features     map[string]bool     `json:"features_active,omitempty"`
	Coalesce     *nic.Coalesce       `json:"coalesce,omitempty"`
	Rings        *nic.Rings          `json:"rings,omitempty"`
	Channels     *nic.Channels       `json:"channels,omitempty"`
	Errors       []string            `json:"errors,omitempty"`
}

// Profile is the host profile document.
type Profile struct {
	Name       string               `json:"name"`
	LabMarker  safety.LabMarker     `json:"lab_marker"`
	Host       sysinfo.Facts        `json:"host"`
	Interfaces map[string]Interface `json:"interfaces"`
	Clock      ClockFacts           `json:"clock"`
	// Measured is filled by the Phase 0 acceptance report from afprobe,
	// kbench and later calibration runs.
	Measured map[string]any `json:"measured"`
}

// ClockFacts records time-keeping configuration.
type ClockFacts struct {
	Adjtimex *clock.Snapshot   `json:"adjtimex,omitempty"`
	Chrony   clock.ChronyConf  `json:"chrony_config"`
	Tracking map[string]string `json:"chrony_tracking,omitempty"`
}

// Build collects the profile. Per-interface errors are recorded inside the
// profile instead of aborting, because virtual NICs legitimately lack some
// ethtool operations.
func Build(name string, ifaces []string, guard safety.Guard, chronyConf string) Profile {
	p := Profile{
		Name:       name,
		LabMarker:  guard.ReadLabMarker(),
		Host:       sysinfo.Collect(),
		Interfaces: map[string]Interface{},
		Measured:   map[string]any{},
	}
	if p.Name == "" {
		p.Name = p.Host.Hostname
	}
	sorted := append([]string(nil), ifaces...)
	sort.Strings(sorted)
	for _, ifname := range sorted {
		p.Interfaces[ifname] = interfaceFacts(ifname)
	}
	if s, err := clock.Read(); err == nil {
		p.Clock.Adjtimex = &s
	}
	if chronyConf == "" {
		chronyConf = clock.DefaultChronyConf
	}
	p.Clock.Chrony, _ = clock.ParseChronyConf(chronyConf)
	p.Clock.Tracking = clock.ChronyTracking()
	return p
}

func interfaceFacts(ifname string) Interface {
	var it Interface
	note := func(err error) {
		if err != nil {
			it.Errors = append(it.Errors, err.Error())
		}
	}
	l, err := nic.LinkFacts(ifname)
	note(err)
	it.Link = l
	if d, err := nic.Driver(ifname); err == nil {
		it.Driver = &d
	} else {
		note(err)
	}
	if t, err := nic.Timestamping(ifname); err == nil {
		it.Timestamping = &t
	} else {
		note(err)
	}
	if h, err := nic.GetHwTstamp(ifname); err == nil {
		it.HwTstamp = &h
	}
	if f, err := nic.Features(ifname); err == nil {
		it.Features = map[string]bool{}
		for _, n := range interestingFeatures {
			if v, ok := f[n]; ok {
				it.Features[n] = v.Active
			}
		}
	} else {
		note(err)
	}
	if c, err := nic.Coalescing(ifname); err == nil {
		it.Coalesce = &c
	}
	if r, err := nic.RingParams(ifname); err == nil {
		it.Rings = &r
	}
	if c, err := nic.ChannelCounts(ifname); err == nil {
		it.Channels = &c
	}
	return it
}
