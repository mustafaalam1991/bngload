package tstamp

import (
	"encoding/binary"
	"testing"

	"github.com/mustafaalam1991/bngload/internal/report"
)

func TestParseFrame(t *testing.T) {
	f := make([]byte, 64)
	p := f[14:]
	copy(p, magic)
	binary.BigEndian.PutUint64(p[8:], 77)
	binary.BigEndian.PutUint32(p[16:], 5)
	if seq, ok := parse(f, 77); !ok || seq != 5 {
		t.Fatalf("parse = %d %v", seq, ok)
	}
	if _, ok := parse(f, 78); ok {
		t.Fatal("frame from another run accepted")
	}
	if _, ok := parse(f[:20], 77); ok {
		t.Fatal("short frame accepted")
	}
}

func TestAnalyseSoftwareOnly(t *testing.T) {
	res := &Result{ModeUsed: ModeSW}
	var ss []Sample
	for i := 0; i < 100; i++ {
		base := int64(1_000_000_000 + i*1000)
		ss = append(ss, Sample{Seq: uint32(i), AppTx: base, TxSW: base + 5000, RxSW: base + 5700, AppRx: base + 14000})
	}
	ss = append(ss, Sample{Seq: 100, AppTx: 1}) // lost frame
	analyse(res, ss)
	a := res.Availability
	if a.Sent != 101 || a.Received != 100 || a.TxSW != 100 || a.RxHW != 0 {
		t.Fatalf("availability %+v", a)
	}
	if res.SoftwareOneWayNs.P50 != 700 || res.AppSendPathNs.P50 != 5000 || res.AppRxWakeupNs.P50 != 8300 {
		t.Fatalf("latencies %+v %+v %+v", res.SoftwareOneWayNs, res.AppSendPathNs, res.AppRxWakeupNs)
	}
	if res.HardwareRxTxVerified {
		t.Fatal("software-only run cannot verify hardware stamps")
	}
	if Verdict(res) != report.Warn {
		t.Fatalf("software-only verdict %s, want WARN", Verdict(res))
	}
}

func TestAnalyseHardwareSamePHC(t *testing.T) {
	res := &Result{ModeUsed: ModeHW, SamePHC: true}
	var ss []Sample
	for i := 0; i < 50; i++ {
		b := int64(1e9 + i*1000)
		ss = append(ss, Sample{AppTx: b, TxSW: b + 10, TxHW: b + 2_000_123, RxHW: b + 2_000_923, RxSW: b + 900, AppRx: b + 5000})
	}
	analyse(res, ss)
	if !res.HardwareRxTxVerified || res.HardwareOneWayNs == nil || res.HardwareOneWayNs.P50 != 800 {
		t.Fatalf("hardware analysis %+v", res.HardwareOneWayNs)
	}
	if !res.HardwareNsResolution {
		t.Fatal("ns resolution not detected")
	}
	if Verdict(res) != report.Pass {
		t.Fatalf("verdict %s", Verdict(res))
	}
}

func TestAnalyseHardwareCrossPHC(t *testing.T) {
	res := &Result{ModeUsed: ModeHW}
	var ss []Sample
	for i := 0; i < 20; i++ {
		b := int64(1e9 + i*1000)
		jit := int64(i % 3)
		ss = append(ss, Sample{AppTx: b, TxSW: b + 1, TxHW: b, RxHW: b + 37_000_000_000 + jit, RxSW: b + 2, AppRx: b + 3})
	}
	analyse(res, ss)
	if res.HardwareOneWayNs != nil {
		t.Fatal("cross-PHC difference must not be reported as latency (MR-1)")
	}
	if res.HardwareCrossPHCJitterNs == nil || res.HardwareCrossPHCJitterNs.Max > 2 {
		t.Fatalf("cross-PHC jitter %+v", res.HardwareCrossPHCJitterNs)
	}
}

func TestConfigDefaultsClampRate(t *testing.T) {
	c := Config{Interval: 1, FrameSize: 9000}
	c.Defaults()
	if c.Interval < 1_000_000 || c.FrameSize != 1514 || c.Count != 200 || c.Mode != ModeAuto {
		t.Fatalf("defaults %+v", c)
	}
}
