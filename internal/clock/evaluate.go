package clock

import (
	"fmt"

	"github.com/mustafaalam1991/bngload/internal/report"
)

// MaxSlewPPM is the slew-rate ceiling required by MR-2.
const MaxSlewPPM = 100.0

// EvaluateConfig checks chrony configuration against MR-2. It is shared by
// `bngload health` (configuration only) and `bngload clock check`.
func EvaluateConfig(conf ChronyConf) []report.Finding {
	var out []report.Finding
	if !conf.Found {
		return append(out, report.Finding{
			ID: "clock.chrony_present", Ref: "MR-2", Title: "chrony configuration present",
			Status: report.Warn, Detail: conf.Path + " not found",
			Remedy: "install chrony (apt install chrony); the design assumes chrony disciplines the system clock",
		})
	}
	switch {
	case conf.MaxSlewRatePPM == nil:
		out = append(out, report.Finding{
			ID: "clock.maxslewrate", Ref: "MR-2", Title: "chrony maxslewrate ≤ 100 ppm",
			Status: report.Warn, Detail: "maxslewrate not set (chrony default is 83333 ppm)",
			Remedy: "add 'maxslewrate 100' to /etc/chrony/conf.d/bngload.conf and restart chrony",
		})
	case *conf.MaxSlewRatePPM > MaxSlewPPM:
		out = append(out, report.Finding{
			ID: "clock.maxslewrate", Ref: "MR-2", Title: "chrony maxslewrate ≤ 100 ppm",
			Status: report.Warn, Detail: fmt.Sprintf("maxslewrate is %.0f ppm", *conf.MaxSlewRatePPM),
			Remedy: "set 'maxslewrate 100'",
		})
	default:
		out = append(out, report.Finding{
			ID: "clock.maxslewrate", Ref: "MR-2", Title: "chrony maxslewrate ≤ 100 ppm",
			Status: report.Pass, Detail: fmt.Sprintf("maxslewrate %.0f ppm", *conf.MaxSlewRatePPM),
		})
	}
	switch {
	case conf.Makestep == nil:
		out = append(out, report.Finding{
			ID: "clock.makestep", Ref: "MR-2", Title: "no clock steps after start-up",
			Status: report.Pass, Detail: "makestep not configured: chrony never steps the clock",
		})
	case conf.Makestep.Limit < 0:
		out = append(out, report.Finding{
			ID: "clock.makestep", Ref: "MR-2", Title: "no clock steps after start-up",
			Status: report.Fail,
			Detail: fmt.Sprintf("makestep %.3g %d allows steps at any time", conf.Makestep.ThresholdS, conf.Makestep.Limit),
			Remedy: "use 'makestep 1 3' (steps only during the first three updates after boot)",
		})
	default:
		out = append(out, report.Finding{
			ID: "clock.makestep", Ref: "MR-2", Title: "no clock steps after start-up",
			Status: report.Pass,
			Detail: fmt.Sprintf("makestep %.3g %d: steps only in the first %d updates", conf.Makestep.ThresholdS, conf.Makestep.Limit, conf.Makestep.Limit),
		})
	}
	return out
}

// EvaluateSnapshot checks that the kernel reports a synchronised clock.
func EvaluateSnapshot(s Snapshot) report.Finding {
	f := report.Finding{ID: "clock.synchronized", Ref: "MR-2", Title: "system clock synchronised"}
	if s.Synchronized {
		f.Status = report.Pass
		f.Detail = fmt.Sprintf("%s, max error %d µs, freq %.3f ppm", s.StateName, s.MaxErrorUs, s.FreqPPM)
	} else {
		f.Status = report.Fail
		f.Detail = fmt.Sprintf("%s, flags %v", s.StateName, s.StatusFlags)
		f.Remedy = "start chronyd (systemctl enable --now chrony) and wait until 'chronyc tracking' shows Leap status Normal"
	}
	return f
}

// EvaluateMonitor checks a Monitor result against MR-2.
func EvaluateMonitor(m MonitorResult) []report.Finding {
	var out []report.Finding
	steps := report.Finding{ID: "clock.no_steps", Ref: "MR-2", Title: "no clock steps during the observation window"}
	if len(m.Steps) == 0 {
		steps.Status = report.Pass
		steps.Detail = fmt.Sprintf("%d samples over %.1f s, jitter p99 %.0f ns", m.Samples, m.DurationS, m.SampleJitterNs.P99)
	} else {
		steps.Status = report.Fail
		steps.Detail = fmt.Sprintf("%d step(s) detected, first %+d ns at %.2f s", len(m.Steps), m.Steps[0].DeltaNs, m.Steps[0].AtS)
		steps.Remedy = "find what steps the clock (makestep, ntpdate, VM tools, manual date) and disable it during runs"
	}
	out = append(out, steps)

	slew := report.Finding{ID: "clock.slew", Ref: "MR-2", Title: "slew deviation ≤ 100 ppm"}
	if m.SlewDeviationPPM <= MaxSlewPPM {
		slew.Status = report.Pass
	} else {
		slew.Status = report.Warn
		slew.Remedy = "lower chrony maxslewrate to 100 ppm"
	}
	slew.Detail = fmt.Sprintf("correction %.3f ppm, max window deviation %.3f ppm", m.CorrectionPPM, m.SlewDeviationPPM)
	out = append(out, slew)

	sync := EvaluateSnapshot(m.Last)
	if m.UnsyncSeen && sync.Status == report.Pass {
		sync.Status = report.Warn
		sync.Detail += " (was unsynchronised at some point during the run)"
	}
	return append(out, sync)
}
