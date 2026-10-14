//go:build integration

// Package integration exercises the Phase 0 tools against real kernel objects.
// It needs root (CAP_NET_ADMIN, CAP_NET_RAW, CAP_SYS_ADMIN) and creates its own
// veth pair, so it never touches a physical port:
//
//	sudo go test -tags integration ./test/integration/ -v
package integration

import (
	"os"
	"testing"
	"time"

	"github.com/mustafaalam1991/bngload/internal/kbench"
	"github.com/mustafaalam1991/bngload/internal/netlinkx"
	"github.com/mustafaalam1991/bngload/internal/report"
	"github.com/mustafaalam1991/bngload/internal/tstamp"
)

const ifA, ifB = "bit0", "bit1"

func requireRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("integration tests need root")
	}
}

// vethPair creates bit0<->bit1 in the root namespace and removes it afterwards.
func vethPair(t *testing.T) {
	t.Helper()
	nl, err := netlinkx.Dial()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { nl.Close() })
	if idx, err := nl.LinkIndex(ifA); err == nil {
		_ = nl.Delete(idx) // left over from an aborted run
	}
	if err := nl.AddVeth(ifA, ifB, -1); err != nil {
		t.Fatalf("create veth: %v", err)
	}
	t.Cleanup(func() {
		if idx, err := nl.LinkIndex(ifA); err == nil {
			_ = nl.Delete(idx)
		}
	})
	for _, n := range []string{ifA, ifB} {
		idx, err := nl.LinkIndex(n)
		if err != nil {
			t.Fatal(err)
		}
		if err := nl.SetUp(idx); err != nil {
			t.Fatal(err)
		}
	}
	// IPv6 DAD and link-local setup are irrelevant to raw 0x88B5 frames, but
	// give the carrier a moment to come up on both ends.
	time.Sleep(100 * time.Millisecond)
}

func TestTimestampSelfTestOnVeth(t *testing.T) {
	requireRoot(t)
	vethPair(t)
	res, err := tstamp.Run(tstamp.Config{TxIf: ifA, RxIf: ifB, Count: 100, Interval: time.Millisecond, Mode: tstamp.ModeAuto})
	if err != nil {
		t.Fatal(err)
	}
	a := res.Availability
	if a.Received != a.Sent || a.Sent != 100 {
		t.Fatalf("frames lost on veth: %+v", a)
	}
	if a.RxSW != a.Received || a.TxSW != a.Sent {
		t.Fatalf("software stamps missing: %+v", a)
	}
	if res.ModeUsed != tstamp.ModeSW {
		t.Fatalf("veth has no PHC, auto should fall back to software, got %s", res.ModeUsed)
	}
	if res.SoftwareOneWayNs.P50 <= 0 || res.SoftwareOneWayNs.P50 > 1e6 {
		t.Fatalf("implausible kernel one-way latency %+v", res.SoftwareOneWayNs)
	}
	if v := tstamp.Verdict(res); v != report.Warn {
		t.Fatalf("software-only verdict %s, want WARN (hardware not verified)", v)
	}
}

func TestTimestampHardwareModeRefusedOnVeth(t *testing.T) {
	requireRoot(t)
	vethPair(t)
	if _, err := tstamp.Run(tstamp.Config{TxIf: ifA, RxIf: ifB, Count: 5, Mode: tstamp.ModeHW}); err == nil {
		t.Fatal("hw mode must fail on a port without hardware timestamping")
	}
}

func TestKbenchScenarios(t *testing.T) {
	requireRoot(t)
	vethPair(t)
	for _, sc := range kbench.Scenarios {
		t.Run(sc, func(t *testing.T) {
			res, err := kbench.Run(kbench.Config{Scenario: sc, Count: 20, Parent: ifA, CleanupTimeout: 10 * time.Second})
			if err != nil {
				t.Fatal(err)
			}
			if res.Created != 20 || res.Error != "" {
				t.Fatalf("created %d/20: %s", res.Created, res.Error)
			}
			if res.CreateUs.Count != 20 || res.CreateUs.P50 <= 0 {
				t.Fatalf("timing summary %+v", res.CreateUs)
			}
		})
	}
	left, err := kbench.Cleanup()
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 0 {
		t.Fatalf("kbench left objects behind: %v", left)
	}
}

func TestNetlinkVethLifecycle(t *testing.T) {
	requireRoot(t)
	nl, err := netlinkx.Dial()
	if err != nil {
		t.Fatal(err)
	}
	defer nl.Close()
	if err := nl.AddVeth("bit9a", "bit9b", -1); err != nil {
		t.Fatal(err)
	}
	a, errA := nl.LinkIndex("bit9a")
	b, errB := nl.LinkIndex("bit9b")
	if errA != nil || errB != nil || a == b {
		t.Fatalf("indexes %d %d %v %v", a, b, errA, errB)
	}
	if err := nl.Delete(a); err != nil {
		t.Fatal(err)
	}
	// Deleting one end removes the pair.
	if _, err := nl.LinkIndex("bit9b"); err == nil {
		t.Fatal("peer survived deletion")
	}
}
