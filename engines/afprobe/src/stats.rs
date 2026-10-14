//! Descriptive statistics, identical in definition to the Go `internal/stats`
//! package so that afprobe and bngload reports can be compared field by field.
//! Percentiles use linear interpolation between closest ranks (Hyndman & Fan
//! type 7, the default of R and NumPy).

use crate::json::Json;

/// Summary of a sample. Keys match the Go `stats.Summary` JSON encoding.
#[derive(Debug, Clone, Default, PartialEq)]
pub struct Summary {
    pub count: usize,
    pub unit: String,
    pub min: f64,
    pub mean: f64,
    pub p50: f64,
    pub p90: f64,
    pub p95: f64,
    pub p99: f64,
    pub p999: f64,
    pub max: f64,
    pub stddev: f64,
}

/// Type-7 percentile of an ascending slice; `p` in 0..=100.
pub fn percentile(sorted: &[f64], p: f64) -> f64 {
    match sorted.len() {
        0 => 0.0,
        1 => sorted[0],
        n => {
            let h = (n - 1) as f64 * (p.clamp(0.0, 100.0) / 100.0);
            let lo = h.floor() as usize;
            let hi = h.ceil() as usize;
            sorted[lo] + (h - lo as f64) * (sorted[hi] - sorted[lo])
        }
    }
}

/// Summarises `values` (not modified).
pub fn summarize(values: &[f64], unit: &str) -> Summary {
    let mut s = Summary { unit: unit.to_string(), count: values.len(), ..Default::default() };
    if values.is_empty() {
        return s;
    }
    let mut v = values.to_vec();
    v.sort_by(|a, b| a.total_cmp(b));
    let n = v.len() as f64;
    s.min = v[0];
    s.max = v[v.len() - 1];
    s.mean = v.iter().sum::<f64>() / n;
    if v.len() > 1 {
        let var = v.iter().map(|x| (x - s.mean).powi(2)).sum::<f64>() / (n - 1.0);
        s.stddev = var.sqrt();
    }
    s.p50 = percentile(&v, 50.0);
    s.p90 = percentile(&v, 90.0);
    s.p95 = percentile(&v, 95.0);
    s.p99 = percentile(&v, 99.0);
    s.p999 = percentile(&v, 99.9);
    s
}

impl Summary {
    pub fn to_json(&self) -> Json {
        Json::obj()
            .with("count", self.count)
            .with("unit", self.unit.as_str())
            .with("min", self.min)
            .with("mean", self.mean)
            .with("p50", self.p50)
            .with("p90", self.p90)
            .with("p95", self.p95)
            .with("p99", self.p99)
            .with("p99_9", self.p999)
            .with("max", self.max)
            .with("stddev", self.stddev)
    }
}

/// `num / den`, or 0 when `den` is 0.
pub fn ratio(num: f64, den: f64) -> f64 {
    if den == 0.0 {
        0.0
    } else {
        num / den
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn type7_matches_go_reference() {
        let v: Vec<f64> = (1..=10).map(f64::from).collect();
        for (p, want) in [(0.0, 1.0), (50.0, 5.5), (90.0, 9.1), (99.0, 9.91), (100.0, 10.0)] {
            assert!((percentile(&v, p) - want).abs() < 1e-9, "p{p}");
        }
    }

    #[test]
    fn summary_basics() {
        let s = summarize(&[5.0, 1.0, 3.0], "us");
        assert_eq!((s.count, s.min, s.max, s.mean, s.p50), (3, 1.0, 5.0, 3.0, 3.0));
        assert!((s.stddev - 2.0).abs() < 1e-9);
        assert_eq!(summarize(&[], "ns").count, 0);
    }

    #[test]
    fn ratio_guards_zero() {
        assert_eq!(ratio(1.0, 0.0), 0.0);
        assert_eq!(ratio(1.0, 4.0), 0.25);
    }
}
