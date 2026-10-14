package clock

import (
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/mustafaalam1991/bngload/internal/linux"
	"github.com/mustafaalam1991/bngload/internal/nic"
	"github.com/mustafaalam1991/bngload/internal/stats"
)

// PHCResult compares a NIC's PTP hardware clock with CLOCK_REALTIME.
//
// bngload never subtracts a PHC timestamp from a system-clock timestamp
// (MR-1), so a large constant offset is harmless — PHCs commonly run on TAI
// (37 s ahead of UTC). What matters is that the PHC exists, is readable and,
// for multi-host runs (MR-5), is disciplined by ptp4l/phc2sys so that the
// offset stays constant (low drift).
type PHCResult struct {
	Interface string `json:"interface"`
	Index     int    `json:"phc_index"`
	Device    string `json:"device"`
	Samples   int    `json:"samples"`
	// OffsetNs summarises PHC − REALTIME, sampled with a REALTIME/PHC/REALTIME
	// sandwich; the read is attributed to the sandwich midpoint.
	OffsetNs stats.Summary `json:"offset_ns"`
	// ReadDelayNs summarises the sandwich width (uncertainty of each sample).
	ReadDelayNs stats.Summary `json:"read_delay_ns"`
	// DriftPPM is the change of the offset over the run divided by its
	// duration. Near zero when phc2sys (or chrony with the PHC as a refclock)
	// keeps the two clocks locked.
	DriftPPM float64 `json:"drift_ppm"`
}

// MeasurePHC samples the PHC of ifname n times, interval apart.
func MeasurePHC(ifname string, n int, interval time.Duration) (PHCResult, error) {
	res := PHCResult{Interface: ifname, Index: -1}
	ts, err := nic.Timestamping(ifname)
	if err != nil {
		return res, err
	}
	if ts.PHCIndex < 0 {
		return res, fmt.Errorf("%s has no PTP hardware clock", ifname)
	}
	res.Index = ts.PHCIndex
	res.Device = fmt.Sprintf("/dev/ptp%d", ts.PHCIndex)
	f, err := os.Open(res.Device)
	if err != nil {
		return res, fmt.Errorf("open %s: %w", res.Device, err)
	}
	defer f.Close()
	cid := linux.FDToClockID(int(f.Fd()))

	type pt struct{ mid, off, delay int64 }
	var pts []pt
	for i := 0; i < n; i++ {
		a, err := linux.ClockGettime(linux.ClockRealtime)
		if err != nil {
			return res, err
		}
		p, err := linux.ClockGettime(cid)
		if err != nil {
			return res, fmt.Errorf("read %s: %w", res.Device, err)
		}
		b, err := linux.ClockGettime(linux.ClockRealtime)
		if err != nil {
			return res, err
		}
		mid := a + (b-a)/2
		pts = append(pts, pt{mid: mid, off: p - mid, delay: b - a})
		if i+1 < n {
			time.Sleep(interval)
		}
	}
	res.Samples = len(pts)
	offs := make([]float64, len(pts))
	delays := make([]float64, len(pts))
	for i, p := range pts {
		offs[i] = float64(p.off)
		delays[i] = float64(p.delay)
	}
	res.OffsetNs = stats.Summarize(offs, "ns")
	res.ReadDelayNs = stats.Summarize(delays, "ns")
	if len(pts) >= 4 {
		// Use the median of the first and last quarter to resist outliers.
		q := len(pts) / 4
		head := medianOff(pts[:q], func(p pt) float64 { return float64(p.off) })
		tail := medianOff(pts[len(pts)-q:], func(p pt) float64 { return float64(p.off) })
		dt := float64(pts[len(pts)-1].mid - pts[0].mid)
		if dt > 0 {
			res.DriftPPM = (tail - head) / dt * 1e6
		}
	}
	return res, nil
}

func medianOff[T any](xs []T, f func(T) float64) float64 {
	v := make([]float64, len(xs))
	for i, x := range xs {
		v[i] = f(x)
	}
	sort.Float64s(v)
	return stats.Percentile(v, 50)
}
