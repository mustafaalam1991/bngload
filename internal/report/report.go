// Package report defines the JSON envelope shared by every Phase 0 tool.
//
// All bngload tools (Go), the afprobe engine (Rust) and the bngload-lab
// harness (Python) write results in the same envelope so that the Phase 0
// acceptance report can aggregate them without tool-specific parsing:
//
//	{
//	  "schema":      "bngload.report/v1",
//	  "tool":        "bngload tstamp check",
//	  "version":     "0.0.0-phase0 (commit …)",
//	  "host":        {"hostname": "lab-gen-01", "kernel": "6.8.0-45-generic"},
//	  "started_at":  "2026-10-14T09:00:00Z",
//	  "finished_at": "2026-10-14T09:00:05Z",
//	  "verdict":     "PASS" | "WARN" | "FAIL" | "INFO",
//	  "summary":     "one-line human summary",
//	  "data":        { tool-specific payload }
//	}
//
// The schema identifier changes only on breaking changes.
package report

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/mustafaalam1991/bngload/internal/version"
)

// Schema is the identifier written into every envelope.
const Schema = "bngload.report/v1"

// Verdict is the overall outcome of a tool run.
type Verdict string

// Verdicts in increasing order of severity (INFO < PASS < WARN < FAIL).
const (
	Info Verdict = "INFO"
	Pass Verdict = "PASS"
	Warn Verdict = "WARN"
	Fail Verdict = "FAIL"
)

// rank orders verdicts by severity so that Worst can combine them.
func (v Verdict) rank() int {
	switch v {
	case Fail:
		return 3
	case Warn:
		return 2
	case Pass:
		return 1
	default:
		return 0
	}
}

// Worst returns the most severe of the given verdicts (INFO if none).
func Worst(vs ...Verdict) Verdict {
	out := Info
	for _, v := range vs {
		if v.rank() > out.rank() {
			out = v
		}
	}
	return out
}

// Finding is one evaluated rule, used by health checks and by the
// pass/fail evaluation of individual tools. Ref names the design rule it
// verifies (for example "MR-2" or "SR-19") so every result is traceable to
// docs/phase0/requirements-traceability.md.
type Finding struct {
	ID     string  `json:"id"`
	Ref    string  `json:"ref,omitempty"`
	Title  string  `json:"title"`
	Status Verdict `json:"status"`
	Detail string  `json:"detail,omitempty"`
	Remedy string  `json:"remedy,omitempty"`
}

// WorstOf returns the most severe status among findings.
func WorstOf(fs []Finding) Verdict {
	out := Info
	for _, f := range fs {
		out = Worst(out, f.Status)
	}
	return out
}

// Host identifies the machine that produced a report.
type Host struct {
	Hostname string `json:"hostname"`
	Kernel   string `json:"kernel"`
}

// Envelope is the common result wrapper.
type Envelope struct {
	Schema     string    `json:"schema"`
	Tool       string    `json:"tool"`
	Version    string    `json:"version"`
	Host       Host      `json:"host"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at"`
	Verdict    Verdict   `json:"verdict"`
	Summary    string    `json:"summary"`
	Data       any       `json:"data"`
}

// New starts an envelope for the named tool and stamps the start time.
func New(tool string) *Envelope {
	h, _ := os.Hostname()
	return &Envelope{
		Schema:    Schema,
		Tool:      tool,
		Version:   version.String(),
		Host:      Host{Hostname: h, Kernel: kernelRelease()},
		StartedAt: time.Now().UTC(),
		Verdict:   Info,
	}
}

// Finish records the outcome and the finish time.
func (e *Envelope) Finish(v Verdict, summary string, data any) {
	e.FinishedAt = time.Now().UTC()
	e.Verdict = v
	e.Summary = summary
	e.Data = data
}

// WriteFile writes the envelope as indented JSON. The write is atomic
// (temporary file + rename) so a crash never leaves a truncated result that
// the acceptance report could misread.
//
// The temporary file gets an unpredictable name and is created exclusively
// (os.CreateTemp: O_CREAT|O_EXCL, never following a symlink). The tools run
// as root, so a fixed name such as "<path>.tmp" in a shared directory would
// let another local user redirect the write to any file on the host.
func (e *Envelope) WriteFile(path string) error {
	b, err := json.MarshalIndent(e, "", "  ")
	if err != nil {
		return fmt.Errorf("encode report: %w", err)
	}
	b = append(b, '\n')
	return WriteFileAtomic(path, b, 0o644)
}

// WriteFileAtomic writes data to path via an exclusively created temporary
// file in the same directory, then renames it into place.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create report directory: %w", err)
	}
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("write report: %w", err)
	}
	tmp := f.Name()
	defer os.Remove(tmp) // no-op after a successful rename
	if _, err := f.Write(data); err != nil {
		f.Close()
		return fmt.Errorf("write report: %w", err)
	}
	if err := f.Chmod(perm); err != nil {
		f.Close()
		return fmt.Errorf("write report: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("write report: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("finalise report: %w", err)
	}
	return nil
}

// kernelRelease returns `uname -r` without shelling out.
func kernelRelease() string {
	var u syscall.Utsname
	if err := syscall.Uname(&u); err != nil {
		return "unknown"
	}
	return CString(u.Release[:])
}

// CString converts a NUL-terminated C char array (as found in Utsname or
// ethtool structures) to a Go string. It accepts both int8 and uint8 arrays.
func CString[T int8 | uint8](b []T) string {
	out := make([]byte, 0, len(b))
	for _, c := range b {
		if c == 0 {
			break
		}
		out = append(out, byte(c))
	}
	return string(out)
}
