package report

import (
	"encoding/json"
	"os"
	"path/filepath"
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

func TestCString(t *testing.T) {
	if CString([]int8{'a', 'b', 0, 'c'}) != "ab" || CString([]uint8{'x'}) != "x" {
		t.Fatal("CString")
	}
}
