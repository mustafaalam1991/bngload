package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/mustafaalam1991/bngload/internal/cli"
	"github.com/mustafaalam1991/bngload/internal/clock"
	"github.com/mustafaalam1991/bngload/internal/health"
	"github.com/mustafaalam1991/bngload/internal/hostprofile"
	"github.com/mustafaalam1991/bngload/internal/kbench"
	"github.com/mustafaalam1991/bngload/internal/report"
	"github.com/mustafaalam1991/bngload/internal/safety"
	"github.com/mustafaalam1991/bngload/internal/tstamp"
	"github.com/mustafaalam1991/bngload/internal/version"
)

// ---------------------------------------------------------------------------
// version
// ---------------------------------------------------------------------------

func versionCommand() *cli.Command {
	return &cli.Command{
		Name:    "version",
		Summary: "print build information",
		Run: func(env *cli.Env, args []string) int {
			fmt.Fprintln(env.Stdout, version.String())
			return cli.ExitOK
		},
	}
}

// ---------------------------------------------------------------------------
// health
// ---------------------------------------------------------------------------

const healthHelp = `Check that this host is ready to act as a bngload generator.

Read-only checks of kernel, privileges, safety interlocks (lab marker,
interface allowlist), CPU isolation, sysctls, clock discipline (MR-2) and,
for every --interface, addressing, MTU, VLAN offload, interrupt coalescing,
ring sizes and hardware/software timestamping. Each finding names the design
rule it verifies and the command that fixes it.`

func healthCommand() *cli.Command {
	return &cli.Command{
		Name:    "health",
		Summary: "host readiness checks (read-only)",
		Help:    healthHelp,
		Run: func(env *cli.Env, args []string) int {
			fs := cli.NewFlagSet(env, "bngload health", healthHelp)
			var ifaces cli.StringList
			fs.Var(&ifaces, "interface", "test port to check (repeatable or comma-separated)")
			out := fs.String("o", "", "write the JSON report to this file")
			strict := fs.Bool("strict", false, "treat WARN as failure (exit code 1)")
			chrony := fs.String("chrony-conf", clock.DefaultChronyConf, "chrony configuration file")
			noQinQ := fs.Bool("no-qinq", false, "skip the QinQ MTU check (single-tag or untagged labs)")
			if code, ok := cli.Parse(fs, args); !ok {
				return code
			}
			env0 := report.New("bngload health")
			res := health.Run(health.Options{Interfaces: ifaces, Guard: safety.FromEnv(), ChronyConf: *chrony, QinQ: !*noQinQ})
			v := res.Verdict()
			env0.Finish(v, countsSummary(res.Counts), res)
			printFindings(env.Stdout, res.Findings)
			fmt.Fprintf(env.Stdout, "\nhealth: %s (%s)\n", v, countsSummary(res.Counts))
			if err := writeOut(env0, *out); err != nil {
				fmt.Fprintln(env.Stderr, err)
				return cli.ExitError
			}
			return cli.ExitFor(v, *strict)
		},
	}
}

// ---------------------------------------------------------------------------
// host profile
// ---------------------------------------------------------------------------

const hostProfileHelp = `Write the host profile: OS, kernel, CPU/NUMA layout, isolation, sysctls,
limits, daemons, tools, and for every --interface the driver, firmware,
timestamping capabilities, offloads, coalescing, rings and queues.

The profile is the Phase 0 deliverable "host profile with NIC timestamp
support verified"; commit it as hosts/<name>.json.`

func hostProfileCommand() *cli.Command {
	return &cli.Command{
		Name:    "profile",
		Summary: "write the host profile JSON",
		Help:    hostProfileHelp,
		Run: func(env *cli.Env, args []string) int {
			fs := cli.NewFlagSet(env, "bngload host profile", hostProfileHelp)
			var ifaces cli.StringList
			fs.Var(&ifaces, "interface", "test port to profile (repeatable)")
			name := fs.String("name", "", "profile name (default: hostname)")
			out := fs.String("o", "", "output file (default hosts/<name>.json)")
			chrony := fs.String("chrony-conf", clock.DefaultChronyConf, "chrony configuration file")
			if code, ok := cli.Parse(fs, args); !ok {
				return code
			}
			env0 := report.New("bngload host profile")
			p := hostprofile.Build(*name, ifaces, safety.FromEnv(), *chrony)
			env0.Finish(report.Info, fmt.Sprintf("host %s, %d interface(s)", p.Name, len(p.Interfaces)), p)
			path := *out
			if path == "" {
				path = "hosts/" + p.Name + ".json"
			}
			if err := env0.WriteFile(path); err != nil {
				fmt.Fprintln(env.Stderr, err)
				return cli.ExitError
			}
			fmt.Fprintf(env.Stdout, "host profile %s written to %s\n", p.Name, path)
			for n, it := range p.Interfaces {
				drv := "unknown driver"
				if it.Driver != nil {
					drv = it.Driver.Driver + " " + it.Driver.Firmware
				}
				caps := "no timestamping info"
				if it.Timestamping != nil {
					caps = strings.Join(it.Timestamping.Capabilities, ", ")
				}
				fmt.Fprintf(env.Stdout, "  %s: %s; %s\n", n, drv, caps)
			}
			return cli.ExitOK
		},
	}
}

// ---------------------------------------------------------------------------
// clock check
// ---------------------------------------------------------------------------

const clockHelp = `Collect MR-2 evidence: sample CLOCK_REALTIME, CLOCK_MONOTONIC and
CLOCK_MONOTONIC_RAW for --duration and report clock steps, frequency
correction and slew deviation; check adjtimex synchronisation state and the
chrony configuration (maxslewrate ≤ 100 ppm, no runtime makestep). With
--phc-interface, also compare that NIC's PTP hardware clock with the system
clock (offset, read uncertainty, drift).`

func clockCheckCommand() *cli.Command {
	return &cli.Command{
		Name:    "check",
		Summary: "verify clock discipline (MR-2)",
		Help:    clockHelp,
		Run: func(env *cli.Env, args []string) int {
			fs := cli.NewFlagSet(env, "bngload clock check", clockHelp)
			dur := fs.Duration("duration", 60*time.Second, "observation window")
			interval := fs.Duration("interval", 100*time.Millisecond, "sampling interval")
			step := fs.Duration("step-threshold", 50*time.Microsecond, "smallest jump reported as a step")
			phc := fs.String("phc-interface", "", "also measure this interface's PTP hardware clock")
			chrony := fs.String("chrony-conf", clock.DefaultChronyConf, "chrony configuration file")
			out := fs.String("o", "", "write the JSON report to this file")
			strict := fs.Bool("strict", false, "treat WARN as failure")
			if code, ok := cli.Parse(fs, args); !ok {
				return code
			}
			env0 := report.New("bngload clock check")
			fmt.Fprintf(env.Stdout, "observing clocks for %s …\n", dur)
			mon, err := clock.Monitor(clock.MonitorConfig{Duration: *dur, Interval: *interval, StepThreshold: *step})
			if err != nil {
				fmt.Fprintln(env.Stderr, err)
				return cli.ExitError
			}
			conf, _ := clock.ParseChronyConf(*chrony)
			findings := append(clock.EvaluateMonitor(mon), clock.EvaluateConfig(conf)...)
			data := map[string]any{"monitor": mon, "chrony_config": conf, "chrony_tracking": clock.ChronyTracking()}
			if *phc != "" {
				r, err := clock.MeasurePHC(*phc, 200, 10*time.Millisecond)
				if err != nil {
					findings = append(findings, report.Finding{ID: "clock.phc", Ref: "MR-3", Title: "PTP hardware clock readable",
						Status: report.Warn, Detail: err.Error()})
				} else {
					findings = append(findings, report.Finding{ID: "clock.phc", Ref: "MR-3", Title: "PTP hardware clock readable",
						Status: report.Pass, Detail: fmt.Sprintf("%s offset p50 %.0f ns, read uncertainty p50 %.0f ns, drift %.3f ppm",
							r.Device, r.OffsetNs.P50, r.ReadDelayNs.P50, r.DriftPPM)})
					data["phc"] = r
				}
			}
			data["findings"] = findings
			v := report.WorstOf(findings)
			env0.Finish(v, fmt.Sprintf("%d steps, correction %.2f ppm, slew deviation %.2f ppm",
				len(mon.Steps), mon.CorrectionPPM, mon.SlewDeviationPPM), data)
			printFindings(env.Stdout, findings)
			fmt.Fprintf(env.Stdout, "\nclock: %s\n", v)
			if err := writeOut(env0, *out); err != nil {
				fmt.Fprintln(env.Stderr, err)
				return cli.ExitError
			}
			return cli.ExitFor(v, *strict)
		},
	}
}

// ---------------------------------------------------------------------------
// tstamp check
// ---------------------------------------------------------------------------

const tstampHelp = `Send self-test frames from --tx-if to --rx-if (two ports cabled
back-to-back) and verify kernel and NIC timestamps on both directions.

Phase 0 acceptance requires hardware stamps on RX and TX. Frames use the
IEEE 802 local experimental Ethertype 0x88B5, unicast to the RX port, at most
1000 frames/s. Both ports must pass the safety interlock (lab marker,
allowlist, no IP address). On NICs with hardware timestamping the test
temporarily enables it (SIOCSHWTSTAMP) and restores the previous setting.`

func tstampCheckCommand() *cli.Command {
	return &cli.Command{
		Name:    "check",
		Summary: "RX/TX timestamp self-test",
		Help:    tstampHelp,
		Run: func(env *cli.Env, args []string) int {
			fs := cli.NewFlagSet(env, "bngload tstamp check", tstampHelp)
			txIf := fs.String("tx-if", "", "transmitting port (required)")
			rxIf := fs.String("rx-if", "", "receiving port (required)")
			count := fs.Int("count", 200, "frames to send")
			interval := fs.Duration("interval", 5*time.Millisecond, "gap between frames (≥ 1ms)")
			mode := fs.String("mode", "auto", "auto | hw | sw")
			size := fs.Int("frame-size", 64, "frame size in bytes (60–1514)")
			out := fs.String("o", "", "write the JSON report to this file")
			keep := fs.Bool("keep-samples", false, "include every per-frame sample in the JSON report")
			strict := fs.Bool("strict", false, "treat WARN (software-only) as failure")
			if code, ok := cli.Parse(fs, args); !ok {
				return code
			}
			if *txIf == "" || *rxIf == "" {
				fmt.Fprintln(env.Stderr, "--tx-if and --rx-if are required")
				return cli.ExitUsage
			}
			if err := safety.FromEnv().RequireTX(*txIf, *rxIf); err != nil {
				fmt.Fprintln(env.Stderr, err)
				return cli.ExitError
			}
			env0 := report.New("bngload tstamp check")
			res, err := tstamp.Run(tstamp.Config{TxIf: *txIf, RxIf: *rxIf, Count: *count,
				Interval: *interval, Mode: tstamp.Mode(*mode), FrameSize: *size})
			if err != nil {
				fmt.Fprintln(env.Stderr, err)
				return cli.ExitError
			}
			if !*keep {
				res.Samples = nil
			}
			v := tstamp.Verdict(res)
			a := res.Availability
			env0.Finish(v, fmt.Sprintf("mode %s, %d/%d received, hw verified %v", res.ModeUsed, a.Received, a.Sent, res.HardwareRxTxVerified), res)
			fmt.Fprintf(env.Stdout, "mode %s: %d/%d frames; stamps tx sw %d hw %d, rx sw %d hw %d\n",
				res.ModeUsed, a.Received, a.Sent, a.TxSW, a.TxHW, a.RxSW, a.RxHW)
			fmt.Fprintf(env.Stdout, "software one-way: p50 %.2f µs p99 %.2f µs | user-space send path p50 %.2f µs, RX wake-up p50 %.2f µs\n",
				res.SoftwareOneWayNs.P50/1e3, res.SoftwareOneWayNs.P99/1e3, res.AppSendPathNs.P50/1e3, res.AppRxWakeupNs.P50/1e3)
			printFindings(env.Stdout, res.Findings)
			fmt.Fprintf(env.Stdout, "\ntstamp: %s\n", v)
			if err := writeOut(env0, *out); err != nil {
				fmt.Fprintln(env.Stderr, err)
				return cli.ExitError
			}
			return cli.ExitFor(v, *strict)
		},
	}
}

// ---------------------------------------------------------------------------
// kbench
// ---------------------------------------------------------------------------

const kbenchHelp = `Measure the kernel cost of per-session isolation objects on this host:
creation latency and rate, kernel memory per object, link-dump slow-down,
teardown time and asynchronous cleanup lag. Scenarios: netns, tierk (netns +
TUN + address + route), veth, macvlan (needs --parent), or all.

The results replace the planning estimates of design section 2.3 (tiers K/F).
Requires CAP_SYS_ADMIN and the lab marker. Creates objects named bkb*.`

func kbenchRunCommand() *cli.Command {
	return &cli.Command{
		Name:    "run",
		Summary: "measure netns/TUN/veth/macvlan costs",
		Help:    kbenchHelp,
		Run: func(env *cli.Env, args []string) int {
			fs := cli.NewFlagSet(env, "bngload kbench run", kbenchHelp)
			scenario := fs.String("scenario", "all", "netns | tierk | veth | macvlan | all")
			count := fs.Int("count", 500, "objects per scenario")
			parent := fs.String("parent", "", "parent interface for the macvlan scenario")
			out := fs.String("o", "", "write the JSON report to this file")
			if code, ok := cli.Parse(fs, args); !ok {
				return code
			}
			guard := safety.FromEnv()
			if err := guard.RequireLabHost(); err != nil {
				fmt.Fprintln(env.Stderr, err)
				return cli.ExitError
			}
			list := []string{*scenario}
			if *scenario == "all" {
				list = nil
				for _, s := range kbench.Scenarios {
					if s == "macvlan" && *parent == "" {
						fmt.Fprintln(env.Stdout, "skipping macvlan (no --parent)")
						continue
					}
					list = append(list, s)
				}
			}
			if *parent != "" {
				if err := guard.RequireTX(*parent); err != nil {
					fmt.Fprintln(env.Stderr, err)
					return cli.ExitError
				}
			}
			stop := make(chan os.Signal, 1)
			signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
			go func() { <-stop; kbench.Interrupt() }()

			env0 := report.New("bngload kbench run")
			var results []*kbench.Result
			verdict := report.Pass
			tw := tabwriter.NewWriter(env.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "SCENARIO\tCREATED\tP50 µs\tP99 µs\tRATE/s\tKiB/OBJ\tLINKDUMP ms\tTEARDOWN ms\tCLEANUP LAG ms\tERROR")
			for _, s := range list {
				r, err := kbench.Run(kbench.Config{Scenario: s, Count: *count, Parent: *parent})
				if err != nil {
					fmt.Fprintln(env.Stderr, err)
					return cli.ExitError
				}
				results = append(results, r)
				if r.Error != "" {
					verdict = report.Worst(verdict, report.Warn)
				}
				fmt.Fprintf(tw, "%s\t%d/%d\t%.0f\t%.0f\t%.0f\t%.1f\t%.2f→%.2f\t%.1f\t%.1f\t%s\n", r.Scenario, r.Created, r.Requested,
					r.CreateUs.P50, r.CreateUs.P99, r.CreateRatePerS, r.KernelMemKiBPerObject,
					r.LinkDumpMsBefore, r.LinkDumpMsAfter, r.TeardownMs, r.CleanupLagMs, r.Error)
			}
			tw.Flush()
			env0.Finish(verdict, fmt.Sprintf("%d scenario(s), %d objects each", len(results), *count), map[string]any{"results": results})
			if err := writeOut(env0, *out); err != nil {
				fmt.Fprintln(env.Stderr, err)
				return cli.ExitError
			}
			return cli.ExitOK
		},
	}
}

func kbenchCleanupCommand() *cli.Command {
	return &cli.Command{
		Name:    "cleanup",
		Summary: "delete links left by an interrupted kbench run",
		Run: func(env *cli.Env, args []string) int {
			fs := cli.NewFlagSet(env, "bngload kbench cleanup", "Delete root-namespace links named "+kbench.Prefix+"*.")
			if code, ok := cli.Parse(fs, args); !ok {
				return code
			}
			removed, err := kbench.Cleanup()
			if err != nil {
				fmt.Fprintln(env.Stderr, err)
				return cli.ExitError
			}
			fmt.Fprintf(env.Stdout, "removed %d link(s) %v\n", len(removed), removed)
			return cli.ExitOK
		},
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func printFindings(w io.Writer, fs []report.Finding) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "STATUS\tREF\tCHECK\tDETAIL")
	for _, f := range fs {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", f.Status, f.Ref, f.Title, f.Detail)
	}
	tw.Flush()
	var rem []report.Finding
	for _, f := range fs {
		if f.Remedy != "" && (f.Status == report.Fail || f.Status == report.Warn) {
			rem = append(rem, f)
		}
	}
	if len(rem) > 0 {
		fmt.Fprintln(w, "\nRemedies:")
		for _, f := range rem {
			fmt.Fprintf(w, "  [%s] %s: %s\n", f.Status, f.Title, f.Remedy)
		}
	}
}

func countsSummary(c map[string]int) string {
	return fmt.Sprintf("%d pass, %d warn, %d fail, %d info", c["PASS"], c["WARN"], c["FAIL"], c["INFO"])
}

func writeOut(e *report.Envelope, path string) error {
	if path == "" {
		return nil
	}
	if err := e.WriteFile(path); err != nil {
		return errors.New("write report: " + err.Error())
	}
	return nil
}
