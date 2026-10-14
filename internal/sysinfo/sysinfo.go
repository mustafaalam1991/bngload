// Package sysinfo collects host facts that affect benchmark validity and
// reproducibility: OS and kernel, CPU and NUMA layout, CPU isolation and
// frequency governor, memory and huge pages, network sysctls, resource
// limits, and which supporting daemons and tools are present.
//
// These facts go into the host profile (hosts/<name>.json) and every run
// manifest (design FR-161) and drive several `bngload health` checks
// (design section 9, host tuning).
//
// All readers resolve paths under Root so unit tests can use a fixture tree.
package sysinfo

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"

	"github.com/mustafaalam1991/bngload/internal/report"
)

// Root is prefixed to every /proc and /sys path (tests use a fixture dir).
var Root = "/"

func path(p string) string { return filepath.Join(Root, p) }

// Facts is the collected host description.
type Facts struct {
	Hostname  string            `json:"hostname"`
	OS        map[string]string `json:"os_release"`
	Kernel    Kernel            `json:"kernel"`
	CPU       CPU               `json:"cpu"`
	Memory    Memory            `json:"memory"`
	Sysctls   map[string]string `json:"sysctls"`
	Limits    Limits            `json:"limits"`
	Processes map[string]bool   `json:"processes"`
	Tools     map[string]string `json:"tools"`
}

// Kernel identifies the running kernel.
type Kernel struct {
	Release string `json:"release"`
	Version string `json:"version"`
	Machine string `json:"machine"`
	Cmdline string `json:"cmdline"`
}

// CPU describes processors and isolation settings.
type CPU struct {
	Model     string `json:"model"`
	Logical   int    `json:"logical_cpus"`
	Online    string `json:"online"`
	Isolated  string `json:"isolated"`
	NohzFull  string `json:"nohz_full"`
	NUMANodes string `json:"numa_nodes"`
	Governor  string `json:"governor"`
}

// Memory describes RAM and huge pages.
type Memory struct {
	TotalKiB    int64 `json:"total_kib"`
	HugePages   int64 `json:"hugepages_total"`
	HugePageKiB int64 `json:"hugepage_size_kib"`
}

// Limits holds resource limits of this process.
type Limits struct {
	NofileSoft  uint64 `json:"nofile_soft"`
	NofileHard  uint64 `json:"nofile_hard"`
	MemlockSoft uint64 `json:"memlock_soft"`
}

// SysctlKeys are the kernel parameters recorded and checked (design 9).
var SysctlKeys = []string{
	"net.core.rmem_max",
	"net.core.wmem_max",
	"net.core.netdev_max_backlog",
	"net.core.netdev_budget",
	"net.core.busy_poll",
	"user.max_net_namespaces",
	"fs.file-max",
	"fs.nr_open",
	"kernel.pid_max",
	"kernel.threads-max",
	"net.netfilter.nf_conntrack_max",
	"kernel.numa_balancing",
}

// watchedProcesses are daemons whose presence matters for timing.
var watchedProcesses = []string{"chronyd", "ntpd", "systemd-timesyncd", "ptp4l", "phc2sys", "irqbalance", "NetworkManager"}

// watchedTools are executables used by Phase 0 workflows.
var watchedTools = []string{"bngblaster", "ethtool", "chronyc", "ptp4l", "phc2sys", "tcpdump", "ip", "afprobe", "bngload-lab"}

// Collect gathers all facts. Missing files yield empty values, never errors:
// a partial profile is more useful than none.
func Collect() Facts {
	f := Facts{
		OS:        OSRelease(),
		Kernel:    KernelInfo(),
		CPU:       CPUInfo(),
		Memory:    MemoryInfo(),
		Sysctls:   map[string]string{},
		Limits:    LimitsInfo(),
		Processes: map[string]bool{},
		Tools:     map[string]string{},
	}
	f.Hostname, _ = os.Hostname()
	for _, k := range SysctlKeys {
		f.Sysctls[k] = Sysctl(k)
	}
	running := RunningProcesses()
	for _, p := range watchedProcesses {
		f.Processes[p] = running[p]
	}
	for _, t := range watchedTools {
		if p, err := exec.LookPath(t); err == nil {
			f.Tools[t] = p
		}
	}
	return f
}

// OSRelease parses /etc/os-release.
func OSRelease() map[string]string {
	out := map[string]string{}
	b, err := os.ReadFile(path("etc/os-release"))
	if err != nil {
		return out
	}
	for _, line := range strings.Split(string(b), "\n") {
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		out[k] = strings.Trim(v, `"'`)
	}
	return out
}

// KernelInfo returns uname fields and the boot command line.
func KernelInfo() Kernel {
	var u syscall.Utsname
	k := Kernel{Cmdline: readTrim("proc/cmdline")}
	if syscall.Uname(&u) == nil {
		k.Release = report.CString(u.Release[:])
		k.Version = report.CString(u.Version[:])
		k.Machine = report.CString(u.Machine[:])
	}
	return k
}

// KernelAtLeast reports whether a release string such as
// "6.8.0-45-generic" is at least major.minor.
func KernelAtLeast(release string, major, minor int) bool {
	parts := strings.SplitN(release, ".", 3)
	if len(parts) < 2 {
		return false
	}
	// Take the leading digits of each part so "6.10-rc1" and "6.10rc1" parse.
	lead := func(s string) string {
		i := 0
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			i++
		}
		return s[:i]
	}
	ma, err1 := strconv.Atoi(lead(parts[0]))
	mi, err2 := strconv.Atoi(lead(parts[1]))
	if err1 != nil || err2 != nil {
		return false
	}
	return ma > major || (ma == major && mi >= minor)
}

// CPUInfo reads processor and isolation facts.
func CPUInfo() CPU {
	c := CPU{
		Logical:   runtime.NumCPU(),
		Online:    readTrim("sys/devices/system/cpu/online"),
		Isolated:  readTrim("sys/devices/system/cpu/isolated"),
		NohzFull:  readTrim("sys/devices/system/cpu/nohz_full"),
		NUMANodes: readTrim("sys/devices/system/node/online"),
		Governor:  readTrim("sys/devices/system/cpu/cpu0/cpufreq/scaling_governor"),
	}
	if c.NohzFull == "(null)" {
		c.NohzFull = ""
	}
	if f, err := os.Open(path("proc/cpuinfo")); err == nil {
		defer f.Close()
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			if k, v, ok := strings.Cut(sc.Text(), ":"); ok && strings.TrimSpace(k) == "model name" {
				c.Model = strings.TrimSpace(v)
				break
			}
		}
	}
	return c
}

// MemoryInfo reads /proc/meminfo.
func MemoryInfo() Memory {
	m := Memory{}
	f, err := os.Open(path("proc/meminfo"))
	if err != nil {
		return m
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fs := strings.Fields(sc.Text())
		if len(fs) < 2 {
			continue
		}
		v, _ := strconv.ParseInt(fs[1], 10, 64)
		switch fs[0] {
		case "MemTotal:":
			m.TotalKiB = v
		case "HugePages_Total:":
			m.HugePages = v
		case "Hugepagesize:":
			m.HugePageKiB = v
		}
	}
	return m
}

// Sysctl returns the value of a dotted sysctl key ("" if absent).
func Sysctl(key string) string {
	return readTrim(filepath.Join("proc/sys", strings.ReplaceAll(key, ".", "/")))
}

// SysctlInt returns a sysctl as an integer, or -1.
func SysctlInt(key string) int64 {
	v, err := strconv.ParseInt(strings.Fields(Sysctl(key) + " x")[0], 10, 64)
	if err != nil {
		return -1
	}
	return v
}

// LimitsInfo reads RLIMIT_NOFILE and RLIMIT_MEMLOCK.
func LimitsInfo() Limits {
	var l Limits
	var r syscall.Rlimit
	if syscall.Getrlimit(syscall.RLIMIT_NOFILE, &r) == nil {
		l.NofileSoft, l.NofileHard = r.Cur, r.Max
	}
	const rlimitMemlock = 8
	if syscall.Getrlimit(rlimitMemlock, &r) == nil {
		l.MemlockSoft = r.Cur
	}
	return l
}

// RunningProcesses returns the set of process names (comm) currently
// running, read from /proc/<pid>/comm.
func RunningProcesses() map[string]bool {
	out := map[string]bool{}
	ents, err := os.ReadDir(path("proc"))
	if err != nil {
		return out
	}
	for _, e := range ents {
		if _, err := strconv.Atoi(e.Name()); err != nil {
			continue
		}
		if comm := readTrim(filepath.Join("proc", e.Name(), "comm")); comm != "" {
			out[comm] = true
		}
	}
	return out
}

// SortedKeys is a small helper for deterministic output.
func SortedKeys[V any](m map[string]V) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

func readTrim(p string) string {
	b, err := os.ReadFile(path(p))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}
