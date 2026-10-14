package safety

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestLabMarkerAndAllowlist(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "lab-host")
	allow := filepath.Join(dir, "interfaces.allow")
	g := Guard{LabMarker: marker, AllowlistPath: allow}

	if g.ReadLabMarker().Present {
		t.Fatal("marker should be absent")
	}
	if err := g.RequireLabHost(); !errors.Is(err, ErrNotPermitted) {
		t.Fatalf("expected refusal, got %v", err)
	}
	_ = os.WriteFile(marker, []byte("# lab\nlab = doha-perf-1\nowner=telco\n"), 0o644)
	m := g.ReadLabMarker()
	if !m.Present || m.Fields["lab"] != "doha-perf-1" || m.Fields["owner"] != "telco" {
		t.Fatalf("marker fields %+v", m)
	}

	if list, err := g.AllowedInterfaces(); err != nil || len(list) != 0 {
		t.Fatalf("missing allowlist should be empty, got %v %v", list, err)
	}
	_ = os.WriteFile(allow, []byte("ens2f1  # second port\n\n# comment\nens2f0\n"), 0o644)
	list, err := g.AllowedInterfaces()
	if err != nil || len(list) != 2 || list[0] != "ens2f0" {
		t.Fatalf("allowlist %v %v", list, err)
	}
	if ok, _ := g.IsAllowed("ens2f1"); !ok {
		t.Fatal("ens2f1 should be allowed")
	}
	if ok, _ := g.IsAllowed("eth0"); ok {
		t.Fatal("eth0 must not be allowed")
	}
}

func TestInlineAllowlistOverridesFile(t *testing.T) {
	g := Guard{AllowlistPath: "/nonexistent", Allowed: []string{"veth0"}}
	if ok, _ := g.IsAllowed("veth0"); !ok {
		t.Fatal("inline allowlist ignored")
	}
}

func TestRequireTXRefusesUnlistedInterface(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "lab-host")
	_ = os.WriteFile(marker, nil, 0o644)
	g := Guard{LabMarker: marker, AllowlistPath: filepath.Join(dir, "none")}
	err := g.RequireTX("lo")
	if !errors.Is(err, ErrNotPermitted) {
		t.Fatalf("lo is not allowlisted and has 127.0.0.1; expected refusal, got %v", err)
	}
}

func TestFromEnv(t *testing.T) {
	t.Setenv("BNGLOAD_LAB_MARKER", "/x/marker")
	t.Setenv("BNGLOAD_ALLOWED_INTERFACES", "a, b")
	g := FromEnv()
	if g.LabMarker != "/x/marker" || len(g.Allowed) != 2 || g.Allowed[1] != "b" {
		t.Fatalf("FromEnv %+v", g)
	}
}
