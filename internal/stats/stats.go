// Package stats provides the small set of descriptive statistics the Phase 0
// tools need: min, mean, percentiles and max of a sample.
//
// Phase 0 samples are small (hundreds to a few thousand values), so exact
// sort-based percentiles are used. The production engine (Phase 6) switches
// to HdrHistogram as described in design section 5.4; the field names here
// match the ones used there so that reports stay comparable.
package stats

import (
	"math"
	"sort"
)

// Summary describes one sample. Percentiles use linear interpolation between
// closest ranks (Hyndman & Fan type 7, the default of numpy and R), which is
// also what the Python harness implements, so Go and Python agree exactly.
type Summary struct {
	Count int     `json:"count"`
	Unit  string  `json:"unit"`
	Min   float64 `json:"min"`
	Mean  float64 `json:"mean"`
	P50   float64 `json:"p50"`
	P90   float64 `json:"p90"`
	P95   float64 `json:"p95"`
	P99   float64 `json:"p99"`
	P999  float64 `json:"p99_9"`
	Max   float64 `json:"max"`
	// StdDev is the sample standard deviation (n-1 denominator).
	StdDev float64 `json:"stddev"`
}

// Summarize computes a Summary. The input slice is not modified.
// An empty input returns a Summary with Count 0 and NaN-free zero fields.
func Summarize(values []float64, unit string) Summary {
	s := Summary{Count: len(values), Unit: unit}
	if len(values) == 0 {
		return s
	}
	v := append([]float64(nil), values...)
	sort.Float64s(v)
	var sum float64
	for _, x := range v {
		sum += x
	}
	s.Min = v[0]
	s.Max = v[len(v)-1]
	s.Mean = sum / float64(len(v))
	s.P50 = Percentile(v, 50)
	s.P90 = Percentile(v, 90)
	s.P95 = Percentile(v, 95)
	s.P99 = Percentile(v, 99)
	s.P999 = Percentile(v, 99.9)
	if len(v) > 1 {
		var ss float64
		for _, x := range v {
			d := x - s.Mean
			ss += d * d
		}
		s.StdDev = math.Sqrt(ss / float64(len(v)-1))
	}
	return s
}

// Percentile returns the p-th percentile (0..100) of an ascending-sorted
// slice using linear interpolation (type 7). It panics on an empty slice
// because that is always a programming error at the call site.
func Percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		panic("stats: percentile of empty sample")
	}
	if p <= 0 {
		return sorted[0]
	}
	if p >= 100 {
		return sorted[len(sorted)-1]
	}
	h := (float64(len(sorted)) - 1) * p / 100
	lo := math.Floor(h)
	hi := math.Ceil(h)
	if lo == hi {
		return sorted[int(lo)]
	}
	return sorted[int(lo)] + (h-lo)*(sorted[int(hi)]-sorted[int(lo)])
}

// Ratio returns num/den, or 0 when den is 0.
func Ratio(num, den int) float64 {
	if den == 0 {
		return 0
	}
	return float64(num) / float64(den)
}
