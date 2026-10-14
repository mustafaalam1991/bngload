// Package safety implements the fail-closed interlocks that every bngload
// tool which transmits frames or changes kernel state must pass first
// (design section 13: SR-11, and the lab-marker and no-IP-address parts of
// SR-19; the other SR-19 checks need the PPPoE engine and come in Phase 1+).
//
// Phase 0 tools apply three checks:
//
//  1. Lab marker: the file /etc/bngload/lab-host must exist. It is created
//     once by the lab administrator and marks the machine as an authorised
//     test host. Without it nothing is transmitted.
//  2. Interface allowlist: an interface may only be used if it is listed in
//     /etc/bngload/interfaces.allow (one name per line, '#' comments).
//  3. No routable address: a test interface must not carry an IPv4 or global
//     IPv6 address. An address is the strongest sign that the port is wired
//     into a routed (possibly production) network.
//
// Paths can be overridden with BNGLOAD_LAB_MARKER and
// BNGLOAD_INTERFACE_ALLOWLIST, and the allowlist can be given inline with
// BNGLOAD_ALLOWED_INTERFACES (comma-separated). The overrides exist for CI
// and unit tests; on a lab host the default files should be used.
package safety

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/mustafaalam1991/bngload/internal/nic"
)

// Default locations of the safety files.
const (
	DefaultLabMarker = "/etc/bngload/lab-host"
	DefaultAllowlist = "/etc/bngload/interfaces.allow"
)

// Guard holds the resolved safety configuration.
type Guard struct {
	LabMarker     string
	AllowlistPath string
	// Allowed is the inline allowlist; when non-empty it replaces the file.
	Allowed []string
}

// FromEnv builds a Guard from defaults and environment overrides.
func FromEnv() Guard {
	g := Guard{LabMarker: DefaultLabMarker, AllowlistPath: DefaultAllowlist}
	if v := os.Getenv("BNGLOAD_LAB_MARKER"); v != "" {
		g.LabMarker = v
	}
	if v := os.Getenv("BNGLOAD_INTERFACE_ALLOWLIST"); v != "" {
		g.AllowlistPath = v
	}
	if v := os.Getenv("BNGLOAD_ALLOWED_INTERFACES"); v != "" {
		for _, p := range strings.Split(v, ",") {
			if p = strings.TrimSpace(p); p != "" {
				g.Allowed = append(g.Allowed, p)
			}
		}
	}
	return g
}

// LabMarker describes the lab marker file. Its optional key=value lines
// (for example "lab=doha-perf-1" or "owner=telco-cloud") are copied into
// reports so that results always state where they were produced.
type LabMarker struct {
	Path    string            `json:"path"`
	Present bool              `json:"present"`
	Fields  map[string]string `json:"fields,omitempty"`
}

// ReadLabMarker reports whether the marker exists and parses its fields.
func (g Guard) ReadLabMarker() LabMarker {
	m := LabMarker{Path: g.LabMarker}
	f, err := os.Open(g.LabMarker)
	if err != nil {
		return m
	}
	defer f.Close()
	m.Present = true
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if k, v, ok := strings.Cut(line, "="); ok {
			if m.Fields == nil {
				m.Fields = map[string]string{}
			}
			m.Fields[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return m
}

// AllowedInterfaces returns the effective allowlist.
func (g Guard) AllowedInterfaces() ([]string, error) {
	if len(g.Allowed) > 0 {
		return append([]string(nil), g.Allowed...), nil
	}
	f, err := os.Open(g.AllowlistPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		if line != "" {
			out = append(out, line)
		}
	}
	sort.Strings(out)
	return out, sc.Err()
}

// IsAllowed reports whether ifname is on the allowlist.
func (g Guard) IsAllowed(ifname string) (bool, error) {
	list, err := g.AllowedInterfaces()
	if err != nil {
		return false, err
	}
	for _, n := range list {
		if n == ifname {
			return true, nil
		}
	}
	return false, nil
}

// ErrNotPermitted wraps every interlock failure so callers can map it to a
// clear message and a non-zero exit code.
var ErrNotPermitted = errors.New("refused by safety interlock")

// RequireTX verifies that frames may be transmitted on all given interfaces.
// It returns an error naming every violated rule and how to fix it.
func (g Guard) RequireTX(ifaces ...string) error {
	var problems []string
	if !g.ReadLabMarker().Present {
		problems = append(problems, fmt.Sprintf(
			"lab marker %s is missing (create it on authorised lab hosts only: sudo install -D -m 0644 /dev/null %s)",
			g.LabMarker, g.LabMarker))
	}
	for _, ifname := range ifaces {
		ok, err := g.IsAllowed(ifname)
		if err != nil {
			problems = append(problems, fmt.Sprintf("cannot read interface allowlist %s: %v", g.AllowlistPath, err))
		} else if !ok {
			problems = append(problems, fmt.Sprintf(
				"interface %s is not in the allowlist %s (add one interface name per line)", ifname, g.AllowlistPath))
		}
		l, err := nic.LinkFacts(ifname)
		if err != nil {
			problems = append(problems, err.Error())
			continue
		}
		if l.HasRoutableAddress() {
			problems = append(problems, fmt.Sprintf(
				"interface %s carries routable addresses %v; a test port must have no IP address (SR-19)",
				ifname, append(l.IPv4, l.IPv6Global...)))
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("%w:\n  - %s", ErrNotPermitted, strings.Join(problems, "\n  - "))
	}
	return nil
}

// RequireLabHost verifies only the lab marker. Used by tools that change
// kernel state (namespaces, links) without transmitting on a test port.
func (g Guard) RequireLabHost() error {
	if !g.ReadLabMarker().Present {
		return fmt.Errorf("%w: lab marker %s is missing", ErrNotPermitted, g.LabMarker)
	}
	return nil
}
