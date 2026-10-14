// Package version holds build metadata for every bngload binary.
//
// The variables are overwritten at link time by the Makefile, for example:
//
//	go build -ldflags "-X github.com/mustafaalam1991/bngload/internal/version.Version=0.1.0" ./cmd/bngload
//
// Every JSON report embeds these values so that a result can always be traced
// back to the exact build that produced it (design FR-161, run manifest).
package version

import (
	"fmt"
	"runtime"
)

var (
	// Version is the semantic version of the build ("0.0.0-phase0" when unset).
	Version = "0.0.0-phase0"
	// Commit is the git commit hash of the build.
	Commit = "unknown"
	// BuildDate is the UTC build timestamp in RFC 3339 format.
	BuildDate = "unknown"
)

// String returns a one-line, human-readable build description.
func String() string {
	return fmt.Sprintf("bngload %s (commit %s, built %s, %s %s/%s)",
		Version, Commit, BuildDate, runtime.Version(), runtime.GOOS, runtime.GOARCH)
}
