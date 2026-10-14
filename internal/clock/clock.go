// Package clock verifies that the generator host's clocks are fit for
// latency measurement (design section 5.2, rules MR-1 to MR-6).
//
// It provides four building blocks used by `bngload clock check` and
// `bngload health`:
//
//   - Read: a snapshot of the kernel time-keeping state via adjtimex(2)
//     (synchronised?, frequency correction, maximum error, leap state).
//   - Monitor: samples CLOCK_REALTIME, CLOCK_MONOTONIC and
//     CLOCK_MONOTONIC_RAW for a period and detects clock steps and slewing.
//     A step is a discontinuity in REALTIME−MONOTONIC; slewing is the rate at
//     which MONOTONIC diverges from the free-running RAW clock.
//   - MeasurePHC: compares a NIC's PTP hardware clock with the system clock.
//   - ParseChronyConf / ChronyTracking: inspect chrony, the NTP daemon the
//     design mandates, for the settings MR-2 requires (no runtime steps,
//     maxslewrate ≤ 100 ppm).
//
// Why it matters: every bngload latency is the difference of two kernel or
// NIC timestamps. A step of the system clock in the middle of an interval
// corrupts that interval silently; MR-2 therefore forbids steps during runs
// and bounds the slew rate so the worst-case distortion of a 100 ms interval
// is 10 µs.
package clock

import (
	"fmt"
	"math"
	"syscall"
	"time"

	"github.com/mustafaalam1991/bngload/internal/linux"
	"github.com/mustafaalam1991/bngload/internal/stats"
)

// adjtimex return values (clock state, from <sys/timex.h>).
var stateNames = map[int]string{
	0: "TIME_OK", 1: "TIME_INS", 2: "TIME_DEL", 3: "TIME_OOP", 4: "TIME_WAIT", 5: "TIME_ERROR",
}

// adjtimex status bits (from <linux/timex.h>).
const (
	staUnsync = 0x0040
	staNano   = 0x2000
)

var statusNames = []struct {
	bit  int32
	name string
}{
	{0x0001, "PLL"}, {0x0002, "PPSFREQ"}, {0x0004, "PPSTIME"}, {0x0008, "FLL"},
	{0x0010, "INS"}, {0x0020, "DEL"}, {0x0040, "UNSYNC"}, {0x0080, "FREQHOLD"},
	{0x0100, "PPSSIGNAL"}, {0x0200, "PPSJITTER"}, {0x0400, "PPSWANDER"}, {0x0800, "PPSERROR"},
	{0x1000, "CLOCKERR"}, {0x2000, "NANO"}, {0x4000, "MODE"}, {0x8000, "CLK"},
}

// Snapshot is a decoded adjtimex(2) read.
type Snapshot struct {
	State        int      `json:"state"`
	StateName    string   `json:"state_name"`
	StatusFlags  []string `json:"status_flags"`
	Synchronized bool     `json:"synchronized"`
	// OffsetNs is the remaining PLL offset being corrected.
	OffsetNs int64 `json:"offset_ns"`
	// FreqPPM is the persistent frequency correction applied to the clock.
	FreqPPM    float64 `json:"freq_ppm"`
	MaxErrorUs int64   `json:"max_error_us"`
	EstErrorUs int64   `json:"est_error_us"`
	TickUs     int64   `json:"tick_us"`
	TAIOffsetS int32   `json:"tai_offset_s"`
}

// Read returns the current kernel time-keeping state.
func Read() (Snapshot, error) {
	var tx syscall.Timex // Modes == 0: read only
	state, err := syscall.Adjtimex(&tx)
	if err != nil {
		return Snapshot{}, fmt.Errorf("adjtimex: %w", err)
	}
	return decode(state, tx), nil
}

func decode(state int, tx syscall.Timex) Snapshot {
	s := Snapshot{
		State:      state,
		StateName:  stateNames[state],
		FreqPPM:    float64(tx.Freq) / 65536.0,
		MaxErrorUs: tx.Maxerror,
		EstErrorUs: tx.Esterror,
		TickUs:     tx.Tick,
		TAIOffsetS: tx.Tai,
	}
	if s.StateName == "" {
		s.StateName = fmt.Sprintf("UNKNOWN(%d)", state)
	}
	for _, f := range statusNames {
		if tx.Status&f.bit != 0 {
			s.StatusFlags = append(s.StatusFlags, f.name)
		}
	}
	if tx.Status&staNano != 0 {
		s.OffsetNs = tx.Offset
	} else {
		s.OffsetNs = tx.Offset * 1000
	}
	s.Synchronized = tx.Status&staUnsync == 0 && state != 5
	return s
}

// MonitorConfig controls a Monitor run.
type MonitorConfig struct {
	Duration time.Duration
	Interval time.Duration
	// StepThreshold is the smallest REALTIME−MONOTONIC jump reported as a
	// clock step. Sampling jitter is a few microseconds, so 50 µs is safe.
	StepThreshold time.Duration
	// Windows is the number of equal windows used to estimate slew changes.
	Windows int
}

// Step is one detected discontinuity of the system clock.
type Step struct {
	// AtS is the time since the start of the monitor (raw clock), seconds.
	AtS float64 `json:"at_s"`
	// DeltaNs is the size of the jump (positive = clock moved forward).
	DeltaNs int64 `json:"delta_ns"`
}

// MonitorResult summarises a Monitor run.
type MonitorResult struct {
	Samples   int     `json:"samples"`
	Discarded int     `json:"discarded_preempted_samples"`
	DurationS float64 `json:"duration_s"`
	Steps     []Step  `json:"steps"`
	// CorrectionPPM is the average rate at which CLOCK_MONOTONIC diverged
	// from CLOCK_MONOTONIC_RAW over the run (frequency correction + slew).
	CorrectionPPM float64 `json:"correction_ppm"`
	// WindowPPM summarises the same rate per window.
	WindowPPM stats.Summary `json:"window_correction_ppm"`
	// SlewDeviationPPM is the largest deviation of a window from the median
	// window rate: a proxy for active offset slewing during the run.
	SlewDeviationPPM float64 `json:"slew_deviation_ppm"`
	// SampleJitterNs summarises |Δ(REALTIME−MONOTONIC)| between consecutive
	// samples excluding steps: the noise floor of the step detector.
	SampleJitterNs stats.Summary `json:"sample_jitter_ns"`
	First          Snapshot      `json:"adjtimex_first"`
	Last           Snapshot      `json:"adjtimex_last"`
	UnsyncSeen     bool          `json:"unsync_seen"`
}

type sample struct{ raw, rt, mono int64 }

// Monitor samples the clocks for cfg.Duration.
func Monitor(cfg MonitorConfig) (MonitorResult, error) {
	if cfg.Interval <= 0 {
		cfg.Interval = 100 * time.Millisecond
	}
	if cfg.StepThreshold <= 0 {
		cfg.StepThreshold = 50 * time.Microsecond
	}
	if cfg.Windows < 2 {
		cfg.Windows = 10
	}
	var res MonitorResult
	first, err := Read()
	if err != nil {
		return res, err
	}
	res.First = first
	res.UnsyncSeen = !first.Synchronized

	var samples []sample
	deadline := time.Now().Add(cfg.Duration)
	for {
		s, ok, err := takeSample()
		if err != nil {
			return res, err
		}
		if ok {
			samples = append(samples, s)
		} else {
			res.Discarded++
		}
		if snap, err := Read(); err == nil && !snap.Synchronized {
			res.UnsyncSeen = true
		}
		if !time.Now().Before(deadline) {
			break
		}
		time.Sleep(cfg.Interval)
	}
	last, err := Read()
	if err != nil {
		return res, err
	}
	res.Last = last
	analyse(&res, samples, cfg)
	return res, nil
}

// takeSample reads RAW, REALTIME, MONOTONIC, RAW. If the two RAW reads are
// more than 50 µs apart the thread was preempted and the sample is dropped.
func takeSample() (sample, bool, error) {
	r1, err := linux.ClockGettime(linux.ClockMonotonicRaw)
	if err != nil {
		return sample{}, false, err
	}
	rt, err := linux.ClockGettime(linux.ClockRealtime)
	if err != nil {
		return sample{}, false, err
	}
	mono, err := linux.ClockGettime(linux.ClockMonotonic)
	if err != nil {
		return sample{}, false, err
	}
	r2, err := linux.ClockGettime(linux.ClockMonotonicRaw)
	if err != nil {
		return sample{}, false, err
	}
	if r2-r1 > 50_000 {
		return sample{}, false, nil
	}
	return sample{raw: (r1 + r2) / 2, rt: rt, mono: mono}, true, nil
}

// analyse fills the derived fields of res from the samples. It is separate
// from Monitor so it can be unit-tested with synthetic clocks.
func analyse(res *MonitorResult, samples []sample, cfg MonitorConfig) {
	res.Samples = len(samples)
	if len(samples) < 2 {
		return
	}
	t0 := samples[0].raw
	res.DurationS = float64(samples[len(samples)-1].raw-t0) / 1e9

	var jitter []float64
	for i := 1; i < len(samples); i++ {
		d0 := samples[i-1].rt - samples[i-1].mono
		d1 := samples[i].rt - samples[i].mono
		jump := d1 - d0
		if abs64(jump) >= cfg.StepThreshold.Nanoseconds() {
			res.Steps = append(res.Steps, Step{AtS: float64(samples[i].raw-t0) / 1e9, DeltaNs: jump})
			continue
		}
		jitter = append(jitter, float64(abs64(jump)))
	}
	res.SampleJitterNs = stats.Summarize(jitter, "ns")

	res.CorrectionPPM = slopePPM(samples[0], samples[len(samples)-1])
	per := len(samples) / cfg.Windows
	if per < 2 {
		per = 2
	}
	var win []float64
	for i := 0; i+per-1 < len(samples); i += per {
		win = append(win, slopePPM(samples[i], samples[i+per-1]))
	}
	res.WindowPPM = stats.Summarize(win, "ppm")
	for _, w := range win {
		if d := math.Abs(w - res.WindowPPM.P50); d > res.SlewDeviationPPM {
			res.SlewDeviationPPM = d
		}
	}
}

// slopePPM returns how fast MONOTONIC ran relative to RAW between a and b.
func slopePPM(a, b sample) float64 {
	dRaw := float64(b.raw - a.raw)
	if dRaw <= 0 {
		return 0
	}
	dCorr := float64((b.mono - b.raw) - (a.mono - a.raw))
	return dCorr / dRaw * 1e6
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
