package report

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorst(t *testing.T) {
	if Worst() != Info || Worst(Pass, Info) != Pass || Worst(Pass, Warn, Info) != Warn || Worst(Warn, Fail, Pass) != Fail {
		t.Fatal("verdict ordering wrong")
	}
	fs := []Finding{{Status: Pass}, {Status: Warn}}
	if WorstOf(fs) != Warn {
		t.Fatal("WorstOf")
	}
}

func TestEnvelopeRoundTrip(t *testing.T) {
	e := New("bngload test")
	e.Finish(Pass, "ok", map[string]int{"n": 1})
	p := filepath.Join(t.TempDir(), "sub", "r.json")
	if err := e.WriteFile(p); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p + ".tmp"); !os.IsNotExist(err) {
		t.Fatal("temporary file left behind")
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got["schema"] != Schema || got["verdict"] != "PASS" || got["tool"] != "bngload test" {
		t.Fatalf("unexpected envelope %v", got)
	}
}

// A pre-planted symlink at the old predictable temporary name must not be
// followed: the target stays untouched.
func TestWriteFileIgnoresPlantedSymlink(t *testing.T) {
	dir := t.TempDir()
	victim := filepath.Join(dir, "victim")
	if err := os.WriteFile(victim, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "r.json")
	if err := os.Symlink(victim, p+".tmp"); err != nil {
		t.Fatal(err)
	}
	if err := New("x").WriteFile(p); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(victim); string(b) != "keep" {
		t.Fatalf("symlink target overwritten: %q", b)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") && e.Name() != "r.json.tmp" {
			t.Fatalf("temporary file left behind: %s", e.Name())
		}
	}
	if fi, err := os.Stat(p); err != nil || fi.Mode().Perm() != 0o644 {
		t.Fatalf("report missing or wrong mode: %v %v", fi, err)
	}
}

func TestCString(t *testing.T) {
	if CString([]int8{'a', 'b', 0, 'c'}) != "ab" || CString([]uint8{'x'}) != "x" {
		t.Fatal("CString")
	}
}
