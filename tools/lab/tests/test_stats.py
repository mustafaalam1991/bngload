import math

from bngload_lab.stats import bootstrap_percentile_ci, percentile, summarize, wilson


def test_type7_matches_go_and_rust_reference():
    v = list(range(1, 11))
    for p, want in [(0, 1), (50, 5.5), (90, 9.1), (99, 9.91), (100, 10)]:
        assert math.isclose(percentile(v, p), want)


def test_summary_keys_and_values():
    s = summarize([5, 1, 3], "ms")
    assert (s.count, s.min, s.max, s.mean, s.p50) == (3, 1, 5, 3, 3)
    assert math.isclose(s.stddev, 2.0)
    assert set(s.to_dict()) == {"count", "unit", "min", "mean", "p50", "p90", "p95", "p99", "p99_9", "max", "stddev"}
    assert summarize([], "ms").count == 0


def test_wilson_known_values():
    w = wilson(0, 10)
    assert w.low == 0 and 0.27 < w.high < 0.28  # ≈ 0.2775
    w = wilson(100, 100)
    assert w.high == 1 and 0.96 < w.low < 0.97  # ≈ 0.9630
    assert wilson(0, 0) is None


def test_bootstrap_is_deterministic_and_brackets_estimate():
    data = [10, 12, 11, 13, 50, 12, 11, 10, 14, 12] * 3
    a = bootstrap_percentile_ci(data, 50)
    b = bootstrap_percentile_ci(data, 50)
    assert a == b
    assert a.low <= a.estimate <= a.high
    assert bootstrap_percentile_ci([1], 50) is None
