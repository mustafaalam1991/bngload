package clock

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/mustafaalam1991/bngload/internal/report"
)

func TestDecodeSnapshot(t *testing.T) {
	tx := syscall.Timex{Freq: 10 * 65536, Status: 0x2000 | 0x1, Offset: 1500, Maxerror: 42}
	s := decode(0, tx)
	if !s.Synchronized || s.FreqPPM != 10 || s.OffsetNs != 1500 || s.StateName != "TIME_OK" {
		t.Fatalf("unexpected snapshot %+v", s)
	}
	tx.Status = staUnsync // microsecond offsets, unsynchronised
	s = decode(5, tx)
	if s.Synchronized || s.OffsetNs != 1_500_000 || s.StateName != "TIME_ERROR" {
		t.Fatalf("unexpected snapshot %+v", s)
	}
}

// synth builds samples for a clock running `ppm` fast with optional steps.
func synth(n int, ppm float64, stepAt int, stepNs int64) []sample {
	out := make([]sample, n)
	var stepped int64
	for i := 0; i < n; i++ {
		raw := int64(i) * int64(100*time.Millisecond)
		corr := int64(float64(raw) * ppm / 1e6)
		if i == stepAt {
			stepped = stepNs
		}
		mono := raw + corr
		out[i] = sample{raw: raw, mono: mono, rt: 1_700_000_000_000_000_000 + mono + stepped}
	}
	return out
}

func TestAnalyseDetectsStepAndRate(t *testing.T) {
	var r MonitorResult
	analyse(&r, synth(101, 12.5, 40, 2_000_000), MonitorConfig{StepThreshold: 50 * time.Microsecond, Windows: 10})
	if len(r.Steps) != 1 || r.Steps[0].DeltaNs != 2_000_000 {
		t.Fatalf("steps %+v", r.Steps)
	}
	if r.CorrectionPPM < 12.4 || r.CorrectionPPM > 12.6 {
		t.Fatalf("correction %v", r.CorrectionPPM)
	}
	if r.SlewDeviationPPM > 0.1 {
		t.Fatalf("constant rate should have no slew deviation, got %v", r.SlewDeviationPPM)
	}
	f := EvaluateMonitor(r)
	if f[0].Status != report.Fail {
		t.Fatalf("a step must fail MR-2: %+v", f[0])
	}
}

func TestAnalyseCleanRun(t *testing.T) {
	var r MonitorResult
	r.Last = Snapshot{Synchronized: true, StateName: "TIME_OK"}
	analyse(&r, synth(50, -3, -1, 0), MonitorConfig{StepThreshold: 50 * time.Microsecond, Windows: 5})
	for _, f := range EvaluateMonitor(r) {
		if f.Status != report.Pass {
			t.Fatalf("clean run should pass: %+v", f)
		}
	}
}

func TestParseChronyConf(t *testing.T) {
	dir := t.TempDir()
	conf := filepath.Join(dir, "chrony.conf")
	confd := filepath.Join(dir, "conf.d")
	_ = os.Mkdir(confd, 0o755)
	_ = os.WriteFile(conf, []byte("# main\npool ntp.ubuntu.com iburst\nmakestep 1 -1\nconfdir "+confd+"\n"), 0o644)
	_ = os.WriteFile(filepath.Join(confd, "bngload.conf"), []byte("maxslewrate 100\nhwtimestamp ens2f0\nrefclock PHC /dev/ptp0 poll 0\n"), 0o644)
	c, err := ParseChronyConf(conf)
	if err != nil {
		t.Fatal(err)
	}
	if !c.Found || c.MaxSlewRatePPM == nil || *c.MaxSlewRatePPM != 100 || c.Makestep == nil || c.Makestep.Limit != -1 {
		t.Fatalf("unexpected parse %+v", c)
	}
	if len(c.HWTimestamp) != 1 || len(c.Refclocks) != 1 || len(c.Sources) != 1 || len(c.Files) != 2 {
		t.Fatalf("unexpected parse %+v", c)
	}
	fs := EvaluateConfig(c)
	if fs[0].Status != report.Pass || fs[1].Status != report.Fail {
		t.Fatalf("maxslewrate should pass and unlimited makestep fail: %+v", fs)
	}
}

func TestParseChronyConfMissing(t *testing.T) {
	c, err := ParseChronyConf(filepath.Join(t.TempDir(), "absent.conf"))
	if err != nil || c.Found {
		t.Fatal("missing file should be Found=false without error")
	}
	if EvaluateConfig(c)[0].Status != report.Warn {
		t.Fatal("missing chrony should warn")
	}
}

func TestParseTrackingCSV(t *testing.T) {
	m := parseTrackingCSV("A9FEA9FE,169.254.169.254,3,1700000000.123,0.000001,0.000002,0.000003,-12.3,0.001,0.010,0.0001,0.0004,64.2,Normal")
	if m["leap_status"] != "Normal" || m["frequency_ppm"] != "-12.3" {
		t.Fatalf("unexpected %v", m)
	}
	if parseTrackingCSV("odd,line")["raw"] == "" {
		t.Fatal("unexpected field count should keep the raw line")
	}
}
