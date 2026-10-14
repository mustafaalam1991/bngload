package stats

import (
	"math"
	"testing"
)

func TestPercentileType7(t *testing.T) {
	v := []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	cases := map[float64]float64{0: 1, 50: 5.5, 90: 9.1, 99: 9.91, 100: 10}
	for p, want := range cases {
		if got := Percentile(v, p); math.Abs(got-want) > 1e-9 {
			t.Errorf("p%v = %v, want %v", p, got, want)
		}
	}
}

func TestSummarize(t *testing.T) {
	s := Summarize([]float64{5, 1, 3}, "us")
	if s.Count != 3 || s.Min != 1 || s.Max != 5 || s.Mean != 3 || s.P50 != 3 || s.Unit != "us" {
		t.Fatalf("unexpected summary %+v", s)
	}
	if math.Abs(s.StdDev-2) > 1e-9 {
		t.Fatalf("stddev %v, want 2", s.StdDev)
	}
}

func TestSummarizeEmptyAndDoesNotMutate(t *testing.T) {
	if s := Summarize(nil, "ns"); s.Count != 0 || s.Max != 0 {
		t.Fatalf("empty summary %+v", s)
	}
	in := []float64{3, 1, 2}
	Summarize(in, "")
	if in[0] != 3 || in[1] != 1 {
		t.Fatal("input slice was sorted in place")
	}
}

func TestRatio(t *testing.T) {
	if Ratio(1, 0) != 0 || Ratio(1, 4) != 0.25 {
		t.Fatal("ratio")
	}
}
