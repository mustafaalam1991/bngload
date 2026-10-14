// Command bngload is the PPPoE BNG workload generator.
//
// Phase 0 ships the lab-validation commands only (design section 15,
// "Requirements and lab validation"):
//
//	bngload health          host readiness checks (read-only)
//	bngload host profile    write the host profile JSON (hosts/<name>.json)
//	bngload clock check     MR-2 clock discipline evidence
//	bngload tstamp check    RX/TX hardware/software timestamp self-test
//	bngload kbench run      measure netns/TUN/veth/macvlan kernel costs
//	bngload kbench cleanup  remove links left by an interrupted kbench run
//	bngload version         build information
//
// Every command prints a human summary and, with -o FILE, writes the common
// JSON envelope (internal/report) consumed by `bngload-lab report`.
// Exit codes: 0 pass, 1 fail, 2 usage error, 3 runtime error.
package main

import (
	"os"

	"github.com/mustafaalam1991/bngload/internal/cli"
)

func main() {
	env := &cli.Env{Stdout: os.Stdout, Stderr: os.Stderr}
	os.Exit(cli.Main(rootCommand(), os.Args[1:], env))
}

func rootCommand() *cli.Command {
	return &cli.Command{
		Name: "bngload",
		Help: `bngload — PPPoE BNG workload generator (Phase 0: lab validation toolkit)

Phase 0 validates the generator host and lab before any PPPoE code runs:
host readiness, NIC timestamping, clock discipline and kernel costs.
See docs/phase0/runbook.md for the full procedure.`,
		Sub: []*cli.Command{
			healthCommand(),
			{Name: "host", Summary: "host profile commands", Sub: []*cli.Command{hostProfileCommand()}},
			{Name: "clock", Summary: "clock discipline commands", Sub: []*cli.Command{clockCheckCommand()}},
			{Name: "tstamp", Summary: "timestamping commands", Sub: []*cli.Command{tstampCheckCommand()}},
			{Name: "kbench", Summary: "kernel cost benchmark", Sub: []*cli.Command{kbenchRunCommand(), kbenchCleanupCommand()}},
			versionCommand(),
		},
	}
}
