// Package health implements `bngload health`: a read-only readiness check
// of the generator host against the design's deployment requirements
// (design sections 2.2, 5.2, 9 and 13).
//
// Every check produces a report.Finding with:
//
//	ID      stable identifier, e.g. "nic.no_ip"
//	Ref     the design rule it verifies, e.g. "SR-19"
//	Status  PASS, WARN (degrades accuracy or scale) or FAIL (must fix)
//	Remedy  the command or setting that fixes it
//
// The checks never change host state. scripts/host-prep.sh applies the
// recommended settings; re-running health afterwards should show PASS.
package health

import (
	"fmt"
	"strings"

	"github.com/mustafaalam1991/bngload/internal/clock"
	"github.com/mustafaalam1991/bngload/internal/linux"
	"github.com/mustafaalam1991/bngload/internal/nic"
	"github.com/mustafaalam1991/bngload/internal/report"
	"github.com/mustafaalam1991/bngload/internal/safety"
	"github.com/mustafaalam1991/bngload/internal/sysinfo"
)

// Options selects what to check.
type Options struct {
	// Interfaces are the test ports to check (may be empty: host checks only).
	Interfaces []string
	Guard      safety.Guard
	ChronyConf string
	// QinQ enables the MTU check for double-tagged frames (default true).
	QinQ bool
}

// Result is the payload of the health report.
type Result struct {
	Interfaces []string         `json:"interfaces"`
	LabMarker  safety.LabMarker `json:"lab_marker"`
	Findings   []report.Finding `json:"findings"`
	Counts     map[string]int   `json:"counts"`
}

// Run executes all checks.
func Run(o Options) *Result {
	res := &Result{Interfaces: o.Interfaces, LabMarker: o.Guard.ReadLabMarker()}
	add := func(f ...report.Finding) { res.Findings = append(res.Findings, f...) }

	add(kernelChecks()...)
	add(privilegeChecks()...)
	add(safetyChecks(o)...)
	add(cpuChecks()...)
	add(sysctlChecks()...)
	add(clockChecks(o.ChronyConf)...)
	add(toolChecks()...)
	for _, ifname := range o.Interfaces {
		add(interfaceChecks(ifname, o.QinQ)...)
	}

	res.Counts = map[string]int{}
	for _, f := range res.Findings {
		res.Counts[string(f.Status)]++
	}
	return res
}

// Verdict is the worst finding status.
func (r *Result) Verdict() report.Verdict { return report.WorstOf(r.Findings) }

func pass(id, ref, title, detail string) report.Finding {
	return report.Finding{ID: id, Ref: ref, Title: title, Status: report.Pass, Detail: detail}
}

func warn(id, ref, title, detail, remedy string) report.Finding {
	return report.Finding{ID: id, Ref: ref, Title: title, Status: report.Warn, Detail: detail, Remedy: remedy}
}

func fail(id, ref, title, detail, remedy string) report.Finding {
	return report.Finding{ID: id, Ref: ref, Title: title, Status: report.Fail, Detail: detail, Remedy: remedy}
}

func info(id, ref, title, detail string) report.Finding {
	return report.Finding{ID: id, Ref: ref, Title: title, Status: report.Info, Detail: detail}
}

// ---------------------------------------------------------------------------

func kernelChecks() []report.Finding {
	k := sysinfo.KernelInfo()
	osr := sysinfo.OSRelease()
	var out []report.Finding
	if sysinfo.KernelAtLeast(k.Release, 5, 15) {
		out = append(out, pass("kernel.version", "ASM-4", "kernel ≥ 5.15", k.Release))
	} else {
		out = append(out, fail("kernel.version", "ASM-4", "kernel ≥ 5.15", k.Release,
			"use Ubuntu 22.04 LTS or newer (24.04 LTS recommended)"))
	}
	if sysinfo.KernelAtLeast(k.Release, 6, 8) {
		out = append(out, info("kernel.afxdp_hw_meta", "2.2", "kernel ≥ 6.8 (AF_XDP hardware TX/RX timestamps for Phase 7)", k.Release))
	} else {
		out = append(out, info("kernel.afxdp_hw_meta", "2.2", "kernel ≥ 6.8 (AF_XDP hardware TX/RX timestamps for Phase 7)",
			k.Release+": AF_XDP hardware timestamp metadata unavailable; not needed before Phase 7"))
	}
	if osr["ID"] == "ubuntu" {
		out = append(out, pass("os.distribution", "", "Ubuntu (Phase 0 target platform)", osr["PRETTY_NAME"]))
	} else {
		out = append(out, warn("os.distribution", "", "Ubuntu (Phase 0 target platform)", osr["PRETTY_NAME"],
			"Phase 0 is tested on Ubuntu 22.04/24.04; other distributions may work"))
	}
	return out
}

func privilegeChecks() []report.Finding {
	caps, err := linux.EffectiveCaps()
	if err != nil {
		return []report.Finding{fail("priv.capabilities", "9", "required capabilities", err.Error(), "")}
	}
	var out []report.Finding
	var missing []string
	for _, c := range []string{"CAP_NET_RAW", "CAP_NET_ADMIN"} {
		if !caps[c] {
			missing = append(missing, c)
		}
	}
	if len(missing) == 0 {
		out = append(out, pass("priv.required", "9", "CAP_NET_RAW and CAP_NET_ADMIN", "present"))
	} else {
		out = append(out, fail("priv.required", "9", "CAP_NET_RAW and CAP_NET_ADMIN", "missing "+strings.Join(missing, ", "),
			"run with sudo, or grant: sudo setcap cap_net_raw,cap_net_admin+ep $(which bngload)"))
	}
	var rec []string
	for _, c := range []string{"CAP_IPC_LOCK", "CAP_SYS_ADMIN", "CAP_BPF"} {
		if !caps[c] {
			rec = append(rec, c)
		}
	}
	if len(rec) == 0 {
		out = append(out, pass("priv.recommended", "9", "CAP_IPC_LOCK, CAP_SYS_ADMIN, CAP_BPF", "present"))
	} else {
		out = append(out, warn("priv.recommended", "9", "CAP_IPC_LOCK, CAP_SYS_ADMIN, CAP_BPF", "missing "+strings.Join(rec, ", "),
			"CAP_SYS_ADMIN is needed by 'kbench'; CAP_IPC_LOCK and CAP_BPF from Phase 2 on"))
	}
	return out
}

func safetyChecks(o Options) []report.Finding {
	var out []report.Finding
	m := o.Guard.ReadLabMarker()
	if m.Present {
		out = append(out, pass("safety.lab_marker", "SR-19", "lab marker present", m.Path))
	} else {
		out = append(out, fail("safety.lab_marker", "SR-19", "lab marker present", m.Path+" missing",
			"on an authorised, isolated lab host only: echo 'lab=<name>' | sudo tee "+m.Path))
	}
	list, err := o.Guard.AllowedInterfaces()
	switch {
	case err != nil:
		out = append(out, fail("safety.allowlist", "SR-11", "interface allowlist readable", err.Error(), ""))
	case len(list) == 0:
		out = append(out, fail("safety.allowlist", "SR-11", "interface allowlist configured", o.Guard.AllowlistPath+" missing or empty",
			"list the test ports, one per line: echo ens2f0 | sudo tee "+o.Guard.AllowlistPath))
	default:
		out = append(out, pass("safety.allowlist", "SR-11", "interface allowlist configured", strings.Join(list, ", ")))
	}
	for _, ifname := range o.Interfaces {
		ok, _ := o.Guard.IsAllowed(ifname)
		if ok {
			out = append(out, pass("safety.allowlisted."+ifname, "SR-11", ifname+" is allowlisted", ""))
		} else {
			out = append(out, fail("safety.allowlisted."+ifname, "SR-11", ifname+" is allowlisted", "not listed",
				"add "+ifname+" to "+o.Guard.AllowlistPath))
		}
	}
	return out
}

func cpuChecks() []report.Finding {
	c := sysinfo.CPUInfo()
	var out []report.Finding
	if c.Isolated != "" {
		out = append(out, pass("cpu.isolation", "9", "isolated CPUs for workers", "isolated="+c.Isolated+" nohz_full="+c.NohzFull))
	} else {
		out = append(out, warn("cpu.isolation", "9", "isolated CPUs for workers", "no isolcpus",
			"add isolcpus=, nohz_full= and rcu_nocbs= for worker cores to the kernel command line"))
	}
	switch c.Governor {
	case "performance":
		out = append(out, pass("cpu.governor", "9", "performance frequency governor", c.Governor))
	case "":
		out = append(out, info("cpu.governor", "9", "performance frequency governor", "cpufreq not exposed (VM or driver)"))
	default:
		out = append(out, warn("cpu.governor", "9", "performance frequency governor", c.Governor,
			"sudo cpupower frequency-set -g performance"))
	}
	if sysinfo.RunningProcesses()["irqbalance"] {
		out = append(out, warn("cpu.irqbalance", "9", "irqbalance not moving test-NIC interrupts", "irqbalance is running",
			"stop irqbalance or ban the test NIC IRQs (IRQBALANCE_BANNED_CPULIST / --banirq), then pin IRQs"))
	} else {
		out = append(out, pass("cpu.irqbalance", "9", "irqbalance not moving test-NIC interrupts", "irqbalance not running"))
	}
	return out
}

func sysctlChecks() []report.Finding {
	var out []report.Finding
	min := func(key string, want int64, ref, remedy string) {
		v := sysinfo.SysctlInt(key)
		title := fmt.Sprintf("%s ≥ %d", key, want)
		switch {
		case v < 0:
			out = append(out, info("sysctl."+key, ref, title, "not available"))
		case v >= want:
			out = append(out, pass("sysctl."+key, ref, title, fmt.Sprint(v)))
		default:
			out = append(out, warn("sysctl."+key, ref, title, fmt.Sprint(v), remedy))
		}
	}
	min("net.core.rmem_max", 64<<20, "9", "sudo sysctl -w net.core.rmem_max=67108864")
	min("net.core.wmem_max", 64<<20, "9", "sudo sysctl -w net.core.wmem_max=67108864")
	min("net.core.netdev_max_backlog", 250000, "9", "sudo sysctl -w net.core.netdev_max_backlog=250000")

	l := sysinfo.LimitsInfo()
	if l.NofileHard >= 1<<20 {
		out = append(out, pass("limits.nofile", "9", "file descriptor hard limit ≥ 1048576", fmt.Sprint(l.NofileHard)))
	} else {
		out = append(out, warn("limits.nofile", "9", "file descriptor hard limit ≥ 1048576", fmt.Sprint(l.NofileHard),
			"set LimitNOFILE=1048576 in the service unit or nofile in /etc/security/limits.d"))
	}
	return out
}

func clockChecks(confPath string) []report.Finding {
	var out []report.Finding
	if s, err := clock.Read(); err == nil {
		out = append(out, clock.EvaluateSnapshot(s))
	} else {
		out = append(out, fail("clock.synchronized", "MR-2", "system clock synchronised", err.Error(), ""))
	}
	if confPath == "" {
		confPath = clock.DefaultChronyConf
	}
	conf, err := clock.ParseChronyConf(confPath)
	if err != nil {
		out = append(out, warn("clock.chrony_config", "MR-2", "chrony configuration readable", err.Error(), ""))
	} else {
		out = append(out, clock.EvaluateConfig(conf)...)
	}
	return out
}

func toolChecks() []report.Finding {
	f := sysinfo.Collect()
	var out []report.Finding
	if p, ok := f.Tools["bngblaster"]; ok {
		out = append(out, pass("tool.bngblaster", "P0", "BNG Blaster installed (Phase 0 baseline runs)", p))
	} else {
		out = append(out, warn("tool.bngblaster", "P0", "BNG Blaster installed (Phase 0 baseline runs)", "not found",
			"scripts/install-bngblaster.sh"))
	}
	if f.Processes["ptp4l"] || f.Processes["phc2sys"] {
		out = append(out, pass("tool.linuxptp", "MR-5", "linuxptp running (multi-host timing)", "ptp4l/phc2sys running"))
	} else {
		out = append(out, info("tool.linuxptp", "MR-5", "linuxptp running (multi-host timing)",
			"not running: fine for single-host Phase 0; required before Phase 8"))
	}
	return out
}

func interfaceChecks(ifname string, qinq bool) []report.Finding {
	id := func(s string) string { return "nic." + ifname + "." + s }
	l, err := nic.LinkFacts(ifname)
	if err != nil {
		return []report.Finding{fail(id("exists"), "2.2", ifname+" exists", err.Error(), "check the interface name")}
	}
	var out []report.Finding
	out = append(out, pass(id("exists"), "2.2", ifname+" exists",
		fmt.Sprintf("index %d, mac %s, numa node %d, pci %s", l.Index, l.MAC, l.NUMANode, l.PCIAddress)))

	if l.OperState == "up" {
		out = append(out, pass(id("up"), "2.2", ifname+" operationally up", fmt.Sprintf("speed %d Mb/s", l.SpeedMbps)))
	} else {
		out = append(out, warn(id("up"), "2.2", ifname+" operationally up", "operstate "+l.OperState,
			"sudo ip link set "+ifname+" up; check cabling to the BNG access port"))
	}

	switch {
	case l.HasRoutableAddress():
		out = append(out, fail(id("no_ip"), "SR-19", ifname+" carries no IP address",
			strings.Join(append(l.IPv4, l.IPv6Global...), ", "),
			"remove addresses (sudo ip addr flush dev "+ifname+") and exclude the port from netplan/NetworkManager"))
	case len(l.IPv6Link) > 0 && !l.IPv6Disabled:
		out = append(out, warn(id("no_ip"), "2.2", ifname+" carries no IP address", "IPv6 link-local present",
			"sudo sysctl -w net.ipv6.conf."+ifname+".disable_ipv6=1 (stops RS/ND frames on the access segment)"))
	default:
		out = append(out, pass(id("no_ip"), "SR-19", ifname+" carries no IP address", "none"))
	}

	if qinq {
		if l.MTU >= 1508 {
			out = append(out, pass(id("mtu"), "2.5", ifname+" MTU ≥ 1508 (QinQ + MRU 1492)", fmt.Sprint(l.MTU)))
		} else {
			out = append(out, warn(id("mtu"), "2.5", ifname+" MTU ≥ 1508 (QinQ + MRU 1492)", fmt.Sprint(l.MTU),
				"AF_PACKET allows only one VLAN tag above the MTU; full-size QinQ PPPoE frames need: sudo ip link set "+ifname+" mtu 1508"))
		}
	}

	if feats, err := nic.Features(ifname); err == nil {
		var on []string
		for _, n := range []string{"rx-vlan-hw-parse", "rx-vlan-stag-hw-parse"} {
			if f, ok := feats[n]; ok && f.Active {
				on = append(on, n)
			}
		}
		if len(on) == 0 {
			out = append(out, pass(id("vlan_offload"), "2.2", ifname+" RX VLAN offload off (tags stay in-frame)", "off"))
		} else {
			out = append(out, warn(id("vlan_offload"), "2.2", ifname+" RX VLAN offload off (tags stay in-frame)", strings.Join(on, ", ")+" on",
				"sudo ethtool -K "+ifname+" rxvlan off rx-vlan-stag-hw-parse off"))
		}
	} else {
		out = append(out, info(id("vlan_offload"), "2.2", ifname+" RX VLAN offload off", err.Error()))
	}

	if c, err := nic.Coalescing(ifname); err == nil {
		if c.RxUsecs == 0 && !c.AdaptiveRx {
			out = append(out, pass(id("coalescing"), "MR-6", ifname+" RX interrupt coalescing off", "rx-usecs 0"))
		} else {
			out = append(out, warn(id("coalescing"), "MR-6", ifname+" RX interrupt coalescing off",
				fmt.Sprintf("rx-usecs %d, adaptive-rx %v", c.RxUsecs, c.AdaptiveRx),
				"sudo ethtool -C "+ifname+" adaptive-rx off rx-usecs 0 (precision runs)"))
		}
	} else {
		out = append(out, info(id("coalescing"), "MR-6", ifname+" RX interrupt coalescing", "not reported by driver: "+err.Error()))
	}

	if r, err := nic.RingParams(ifname); err == nil && r.RxMax > 0 {
		if r.Rx >= r.RxMax {
			out = append(out, pass(id("rings"), "9", ifname+" RX ring at maximum", fmt.Sprintf("%d/%d", r.Rx, r.RxMax)))
		} else {
			out = append(out, warn(id("rings"), "9", ifname+" RX ring at maximum", fmt.Sprintf("%d/%d", r.Rx, r.RxMax),
				fmt.Sprintf("sudo ethtool -G %s rx %d tx %d", ifname, r.RxMax, r.TxMax)))
		}
	}

	if d, err := nic.Driver(ifname); err == nil {
		out = append(out, info(id("driver"), "FR-161", ifname+" driver and firmware",
			fmt.Sprintf("%s %s, firmware %s, bus %s", d.Driver, d.Version, d.Firmware, d.BusInfo)))
	}

	ts, err := nic.Timestamping(ifname)
	if err != nil {
		return append(out, fail(id("ts_software"), "MR-1", ifname+" software timestamping", err.Error(), ""))
	}
	if ts.HasSoftware() {
		out = append(out, pass(id("ts_software"), "MR-1", ifname+" software RX/TX timestamping", strings.Join(ts.Capabilities, ", ")))
	} else {
		out = append(out, fail(id("ts_software"), "MR-1", ifname+" software RX/TX timestamping", strings.Join(ts.Capabilities, ", "),
			"driver lacks software TX stamps; choose another NIC/driver for the test port"))
	}
	if ts.HasHardware() {
		out = append(out, pass(id("ts_hardware"), "MR-3", ifname+" hardware RX/TX timestamping",
			fmt.Sprintf("PHC /dev/ptp%d, tx types %v", ts.PHCIndex, ts.TxTypes)))
		if ts.HwFilterAllSupported() {
			out = append(out, pass(id("ts_filter_all"), "MR-3", ifname+" can hardware-stamp all RX frames", strings.Join(ts.RxFilters, ", ")))
		} else {
			out = append(out, warn(id("ts_filter_all"), "MR-3", ifname+" can hardware-stamp all RX frames", strings.Join(ts.RxFilters, ", "),
				"this NIC stamps only PTP frames in hardware; PPPoE RX stamps will be software (≈µs error). Prefer a NIC with rx filter 'all'"))
		}
	} else {
		out = append(out, warn(id("ts_hardware"), "MR-3", ifname+" hardware RX/TX timestamping", "not advertised",
			"use a PTP-capable NIC port; software stamps work with coalescing off but are less accurate"))
	}
	return out
}
