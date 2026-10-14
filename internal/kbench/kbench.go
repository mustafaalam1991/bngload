// Package kbench measures what per-session kernel objects cost on this host
// (`bngload kbench run`), replacing the planning estimates in design
// section 2.3 with numbers measured in Phase 0.
//
// Scenarios (each creates Count objects, then tears them down):
//
//	netns    one network namespace per session (bare)
//	tierk    the design's tier K context: netns + TUN device inside it,
//	         link up, a /32 address and a default route via the TUN
//	veth     netns + veth pair whose peer end lives in the namespace
//	         (host end stays in the root namespace and is brought up)
//	macvlan  netns + macvlan on a parent port, created inside the namespace
//	         (the design's tier F network attachment); needs --parent
//
// For every scenario it reports per-object creation latency (p50/p99/max),
// creation throughput, kernel memory per object (Slab + Percpu +
// KernelStack + PageTables from /proc/meminfo), how long `RTM_GETLINK` dumps
// take in the root namespace once the objects exist (the "ip link gets slow"
// effect), teardown time and cleanup lag: the time until the kernel has
// finished asynchronously destroying the namespaces (cleanup_net).
//
// All objects are created with the name prefix "bkb" and namespace file
// descriptors held by this process only, so an interrupted run leaves no
// namespaces behind. Root-namespace veth ends left by a crash can be removed
// with `bngload kbench cleanup`.
package kbench

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"github.com/mustafaalam1991/bngload/internal/linux"
	"github.com/mustafaalam1991/bngload/internal/netlinkx"
	"github.com/mustafaalam1991/bngload/internal/stats"
)

// Prefix starts the name of every link kbench creates.
const Prefix = "bkb"

// Scenarios in the order `--scenario all` runs them.
var Scenarios = []string{"netns", "tierk", "veth", "macvlan"}

// Config controls a run.
type Config struct {
	Scenario string `json:"scenario"`
	Count    int    `json:"count"`
	// Parent is the parent interface for the macvlan scenario.
	Parent string `json:"parent,omitempty"`
	// CleanupTimeout bounds the wait for asynchronous namespace cleanup.
	CleanupTimeout time.Duration `json:"cleanup_timeout_ns"`
}

// Result is the outcome of one scenario.
type Result struct {
	Scenario  string `json:"scenario"`
	Requested int    `json:"requested"`
	Created   int    `json:"created"`
	// Error is set when creation stopped early (e.g. a kernel limit).
	Error string `json:"error,omitempty"`
	// CreateUs summarises the creation time of one complete object.
	CreateUs stats.Summary `json:"create_us"`
	// StepsUs breaks creation into its steps (netns, tun, link_up, …).
	StepsUs map[string]stats.Summary `json:"steps_us"`
	// CreateRatePerS is objects created per second (wall clock).
	CreateRatePerS float64 `json:"create_rate_per_s"`
	// KernelMemKiBPerObject is the kernel memory growth per object.
	KernelMemKiBPerObject float64 `json:"kernel_mem_kib_per_object"`
	// LinkDumpMs is the duration of a full RTM_GETLINK dump in the root
	// namespace before creation and with all objects present.
	LinkDumpMsBefore float64 `json:"link_dump_ms_before"`
	LinkDumpMsAfter  float64 `json:"link_dump_ms_after"`
	// TeardownMs is the time to release all objects from this process.
	TeardownMs float64 `json:"teardown_ms"`
	// CleanupLagMs is the extra time until the kernel finished destroying
	// them (root-namespace veth ends gone, or memory returned), -1 if the
	// timeout expired first.
	CleanupLagMs float64 `json:"cleanup_lag_ms"`
	CleanupBasis string  `json:"cleanup_basis"`
}

// object is one created per-session context.
type object struct {
	nsFd     int
	tunFd    int
	hostName string // root-namespace veth end, if any
}

var interrupted atomic.Bool

// Interrupt asks a running benchmark to stop creating objects and tear down.
func Interrupt() { interrupted.Store(true) }

// Run executes one scenario. It locks the goroutine to its OS thread for the
// whole run because namespaces are a per-thread property.
func Run(cfg Config) (*Result, error) {
	if cfg.Count <= 0 {
		cfg.Count = 200
	}
	if cfg.CleanupTimeout <= 0 {
		cfg.CleanupTimeout = 60 * time.Second
	}
	switch cfg.Scenario {
	case "netns", "tierk", "veth":
	case "macvlan":
		if cfg.Parent == "" {
			return nil, errors.New("macvlan scenario needs --parent <interface>")
		}
	default:
		return nil, fmt.Errorf("unknown scenario %q (want one of %v)", cfg.Scenario, Scenarios)
	}
	raiseNofile()
	interrupted.Store(false)

	runtime.LockOSThread()
	// Deliberately no UnlockOSThread: if anything below leaves the thread in
	// the wrong namespace, Go discards the thread when the goroutine exits.

	root, err := os.Open("/proc/thread-self/ns/net")
	if err != nil {
		return nil, fmt.Errorf("open own network namespace: %w", err)
	}
	defer root.Close()
	rootNL, err := netlinkx.Dial()
	if err != nil {
		return nil, err
	}
	defer rootNL.Close()
	if err := ensureNoLeftovers(rootNL); err != nil {
		return nil, err
	}

	res := &Result{Scenario: cfg.Scenario, Requested: cfg.Count, StepsUs: map[string]stats.Summary{}}
	parentIdx := 0
	if cfg.Scenario == "macvlan" {
		if parentIdx, err = rootNL.LinkIndex(cfg.Parent); err != nil {
			return nil, err
		}
	}
	res.LinkDumpMsBefore = linkDumpMs(rootNL)
	memBefore := settledKernelMemKiB(5 * time.Second)

	steps := map[string][]float64{}
	var total []float64
	objs := make([]object, 0, cfg.Count)
	start := time.Now()
	for i := 0; i < cfg.Count && !interrupted.Load(); i++ {
		o, st, err := create(cfg.Scenario, i, int(root.Fd()), rootNL, parentIdx)
		if err != nil {
			res.Error = fmt.Sprintf("object %d: %v", i, err)
			if o.nsFd > 0 {
				objs = append(objs, o) // keep for teardown
			}
			break
		}
		objs = append(objs, o)
		var sum float64
		for k, v := range st {
			steps[k] = append(steps[k], v)
			sum += v
		}
		total = append(total, sum)
	}
	elapsed := time.Since(start)
	res.Created = len(total)
	if elapsed > 0 {
		res.CreateRatePerS = float64(res.Created) / elapsed.Seconds()
	}
	res.CreateUs = stats.Summarize(total, "us")
	for k, v := range steps {
		res.StepsUs[k] = stats.Summarize(v, "us")
	}
	res.LinkDumpMsAfter = linkDumpMs(rootNL)
	if res.Created > 0 {
		res.KernelMemKiBPerObject = float64(kernelMemKiB()-memBefore) / float64(res.Created)
	}

	// Teardown: closing a TUN fd destroys the device; closing the last
	// reference to a namespace queues it for asynchronous destruction, which
	// also destroys the links inside it (and the peer end of a veth pair).
	t0 := time.Now()
	for _, o := range objs {
		if o.tunFd > 0 {
			syscall.Close(o.tunFd)
		}
		if o.nsFd > 0 {
			syscall.Close(o.nsFd)
		}
	}
	res.TeardownMs = ms(time.Since(t0))
	res.CleanupLagMs, res.CleanupBasis = waitCleanup(rootNL, objs, memBefore, cfg.CleanupTimeout)
	return res, nil
}

// create builds one object for the scenario and returns per-step timings in
// microseconds. The thread is always back in the root namespace on return.
func create(scenario string, i int, rootFd int, rootNL *netlinkx.Conn, parent int) (o object, st map[string]float64, err error) {
	st = map[string]float64{}
	lap := time.Now()
	step := func(name string) {
		st[name] = float64(time.Since(lap).Nanoseconds()) / 1e3
		lap = time.Now()
	}

	if err = syscall.Unshare(syscall.CLONE_NEWNET); err != nil {
		return o, nil, fmt.Errorf("unshare(CLONE_NEWNET): %w", err)
	}
	defer func() {
		if e := linux.Setns(rootFd, syscall.CLONE_NEWNET); e != nil && err == nil {
			err = fmt.Errorf("return to root namespace: %w", e)
		}
	}()
	nsFd, err := syscall.Open("/proc/thread-self/ns/net", syscall.O_RDONLY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return o, nil, fmt.Errorf("hold namespace: %w", err)
	}
	o.nsFd = nsFd
	step("netns")

	switch scenario {
	case "tierk":
		name := fmt.Sprintf("%st%d", Prefix, i)
		if o.tunFd, err = openTun(name); err != nil {
			return o, nil, err
		}
		step("tun")
		var nl *netlinkx.Conn
		if nl, err = netlinkx.Dial(); err != nil {
			return o, nil, err
		}
		defer nl.Close()
		var idx int
		if idx, err = nl.LinkIndex(name); err != nil {
			return o, nil, err
		}
		if err = nl.SetUp(idx); err != nil {
			return o, nil, fmt.Errorf("tun up: %w", err)
		}
		step("link_up")
		ip := net.IPv4(100, 64+byte(i>>16), byte(i>>8), byte(i))
		if err = nl.AddAddr(idx, ip, 32); err != nil {
			return o, nil, fmt.Errorf("tun address: %w", err)
		}
		step("address")
		if err = nl.AddDefaultRouteDev(idx, false); err != nil {
			return o, nil, fmt.Errorf("default route: %w", err)
		}
		step("route")
	case "veth":
		host := fmt.Sprintf("%sh%d", Prefix, i)
		peer := fmt.Sprintf("%sn%d", Prefix, i)
		// Return to the root namespace to create the pair there with the
		// peer placed straight into the session namespace.
		if err = linux.Setns(rootFd, syscall.CLONE_NEWNET); err != nil {
			return o, nil, err
		}
		if err = rootNL.AddVeth(host, peer, nsFd); err != nil {
			return o, nil, fmt.Errorf("veth: %w", err)
		}
		o.hostName = host
		step("veth")
		var idx int
		if idx, err = rootNL.LinkIndex(host); err != nil {
			return o, nil, err
		}
		if err = rootNL.SetUp(idx); err != nil {
			return o, nil, err
		}
		step("link_up")
	case "macvlan":
		name := fmt.Sprintf("%sm%d", Prefix, i)
		if err = linux.Setns(rootFd, syscall.CLONE_NEWNET); err != nil {
			return o, nil, err
		}
		if err = rootNL.AddMacvlan(name, parent, nsFd, netlinkx.MacvlanModeBridge); err != nil {
			return o, nil, fmt.Errorf("macvlan: %w", err)
		}
		step("macvlan")
	}
	return o, st, nil
}

// tun ioctl structure: struct ifreq with ifr_flags.
type ifreqFlags struct {
	name  [syscall.IFNAMSIZ]byte
	flags uint16
	_     [22]byte
}

// openTun creates a non-persistent TUN device in the current namespace. The
// device lives exactly as long as the returned file descriptor.
func openTun(name string) (int, error) {
	fd, err := syscall.Open("/dev/net/tun", syscall.O_RDWR|syscall.O_CLOEXEC, 0)
	if err != nil {
		return 0, fmt.Errorf("open /dev/net/tun: %w", err)
	}
	var req ifreqFlags
	copy(req.name[:], name)
	req.flags = syscall.IFF_TUN | syscall.IFF_NO_PI
	if err := linux.Ioctl(fd, syscall.TUNSETIFF, unsafe.Pointer(&req)); err != nil {
		syscall.Close(fd)
		return 0, fmt.Errorf("TUNSETIFF %s: %w", name, err)
	}
	return fd, nil
}

// waitCleanup measures how long the kernel needs to finish destroying the
// namespaces after the last reference was dropped. With veth pairs the
// root-namespace ends disappear when cleanup_net has run, which gives a
// precise signal; otherwise kernel memory returning to within 5 % of the
// pre-run level is used.
func waitCleanup(nl *netlinkx.Conn, objs []object, memBefore int64, timeout time.Duration) (float64, string) {
	start := time.Now()
	hosts := map[string]bool{}
	for _, o := range objs {
		if o.hostName != "" {
			hosts[o.hostName] = true
		}
	}
	for time.Since(start) < timeout {
		if len(hosts) > 0 {
			links, err := nl.Links()
			if err == nil {
				left := 0
				for _, l := range links {
					if hosts[l.Name] {
						left++
					}
				}
				if left == 0 {
					return ms(time.Since(start)), "root-namespace veth ends removed"
				}
			}
		} else {
			now := kernelMemKiB()
			if memBefore <= 0 || float64(now-memBefore) <= 0.05*float64(memBefore) {
				return ms(time.Since(start)), "kernel memory back within 5 % of baseline"
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	return -1, "timeout"
}

// Cleanup deletes root-namespace links whose names start with Prefix.
func Cleanup() ([]string, error) {
	nl, err := netlinkx.Dial()
	if err != nil {
		return nil, err
	}
	defer nl.Close()
	links, err := nl.Links()
	if err != nil {
		return nil, err
	}
	var removed []string
	for _, l := range links {
		if strings.HasPrefix(l.Name, Prefix) {
			if err := nl.Delete(l.Index); err == nil {
				removed = append(removed, l.Name)
			}
		}
	}
	return removed, nil
}

func ensureNoLeftovers(nl *netlinkx.Conn) error {
	links, err := nl.Links()
	if err != nil {
		return err
	}
	for _, l := range links {
		if strings.HasPrefix(l.Name, Prefix) {
			return fmt.Errorf("link %s from an earlier run exists; run 'bngload kbench cleanup' first", l.Name)
		}
	}
	return nil
}

func linkDumpMs(nl *netlinkx.Conn) float64 {
	t := time.Now()
	if _, err := nl.Links(); err != nil {
		return -1
	}
	return ms(time.Since(t))
}

// kernelMemKiB sums the /proc/meminfo fields that grow with kernel objects.
func kernelMemKiB() int64 {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return -1
	}
	defer f.Close()
	want := map[string]bool{"Slab:": true, "Percpu:": true, "KernelStack:": true, "PageTables:": true}
	var sum int64
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fs := strings.Fields(sc.Text())
		if len(fs) >= 2 && want[fs[0]] {
			v, _ := strconv.ParseInt(fs[1], 10, 64)
			sum += v
		}
	}
	return sum
}

// settledKernelMemKiB waits (up to max) until kernel memory stops changing
// by more than 0.5 % between two reads 200 ms apart, so that a previous
// scenario's asynchronous cleanup does not skew this scenario's baseline.
func settledKernelMemKiB(max time.Duration) int64 {
	prev := kernelMemKiB()
	deadline := time.Now().Add(max)
	for time.Now().Before(deadline) {
		time.Sleep(200 * time.Millisecond)
		cur := kernelMemKiB()
		if prev > 0 && absInt(cur-prev) <= prev/200 {
			return cur
		}
		prev = cur
	}
	return prev
}

func absInt(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

// raiseNofile lifts the soft file-descriptor limit to the hard limit: every
// namespace and TUN device holds a descriptor.
func raiseNofile() {
	var r syscall.Rlimit
	if syscall.Getrlimit(syscall.RLIMIT_NOFILE, &r) == nil && r.Cur < r.Max {
		r.Cur = r.Max
		_ = syscall.Setrlimit(syscall.RLIMIT_NOFILE, &r)
	}
}

func ms(d time.Duration) float64 { return float64(d.Nanoseconds()) / 1e6 }
