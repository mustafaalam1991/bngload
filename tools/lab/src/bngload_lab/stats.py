"""Statistics for small Phase 0 samples.

* Percentiles use linear interpolation between closest ranks (Hyndman & Fan
  type 7), the same definition as the Go and Rust tools.
* Confidence intervals for percentiles use the percentile bootstrap with a
  fixed seed, so the same data always gives the same report (design 5.4).
* Success ratios use the Wilson score interval, which behaves well at 0 % and
  100 % where the normal approximation collapses.
"""

from __future__ import annotations

import math
import random
from dataclasses import asdict, dataclass
from typing import Sequence


def percentile(sorted_values: Sequence[float], p: float) -> float:
    """Type-7 percentile of an ascending sequence, ``p`` in 0..100."""
    n = len(sorted_values)
    if n == 0:
        return 0.0
    if n == 1:
        return float(sorted_values[0])
    h = (n - 1) * min(max(p, 0.0), 100.0) / 100.0
    lo, hi = math.floor(h), math.ceil(h)
    return sorted_values[lo] + (h - lo) * (sorted_values[hi] - sorted_values[lo])


@dataclass
class Summary:
    count: int
    unit: str
    min: float = 0.0
    mean: float = 0.0
    p50: float = 0.0
    p90: float = 0.0
    p95: float = 0.0
    p99: float = 0.0
    p99_9: float = 0.0
    max: float = 0.0
    stddev: float = 0.0

    def to_dict(self) -> dict:
        return asdict(self)


def summarize(values: Sequence[float], unit: str) -> Summary:
    """Summary with the same keys as the Go ``stats.Summary``."""
    v = sorted(float(x) for x in values)
    s = Summary(count=len(v), unit=unit)
    if not v:
        return s
    n = len(v)
    s.min, s.max = v[0], v[-1]
    s.mean = sum(v) / n
    if n > 1:
        s.stddev = math.sqrt(sum((x - s.mean) ** 2 for x in v) / (n - 1))
    s.p50, s.p90, s.p95 = percentile(v, 50), percentile(v, 90), percentile(v, 95)
    s.p99, s.p99_9 = percentile(v, 99), percentile(v, 99.9)
    return s


@dataclass
class Interval:
    estimate: float
    low: float
    high: float
    confidence: float
    method: str

    @property
    def width(self) -> float:
        return self.high - self.low

    def to_dict(self) -> dict:
        return asdict(self)


def bootstrap_percentile_ci(
    values: Sequence[float], p: float, confidence: float = 0.95, resamples: int = 2000, seed: int = 1
) -> Interval | None:
    """Percentile-bootstrap CI for the ``p``-th percentile, or None if < 2 values.

    With small samples a high percentile's interval is wide (and for p99 with
    fewer than ~100 values it is essentially the sample maximum); the report
    shows the interval so the reader can see that, rather than hiding it.
    """
    v = sorted(float(x) for x in values)
    if len(v) < 2:
        return None
    rng = random.Random(seed)
    n = len(v)
    est = []
    for _ in range(resamples):
        sample = sorted(v[rng.randrange(n)] for _ in range(n))
        est.append(percentile(sample, p))
    est.sort()
    alpha = (1 - confidence) / 2
    return Interval(
        estimate=percentile(v, p),
        low=percentile(est, 100 * alpha),
        high=percentile(est, 100 * (1 - alpha)),
        confidence=confidence,
        method="percentile bootstrap",
    )


def wilson(successes: int, trials: int, confidence: float = 0.95) -> Interval | None:
    """Wilson score interval for a binomial proportion."""
    if trials <= 0:
        return None
    z = _z(confidence)
    phat = successes / trials
    denom = 1 + z * z / trials
    centre = (phat + z * z / (2 * trials)) / denom
    half = z * math.sqrt(phat * (1 - phat) / trials + z * z / (4 * trials * trials)) / denom
    low = 0.0 if successes == 0 else max(0.0, centre - half)
    high = 1.0 if successes == trials else min(1.0, centre + half)
    return Interval(phat, low, high, confidence, "Wilson score")


def _z(confidence: float) -> float:
    """Two-sided standard-normal quantile (inverse error function by bisection)."""
    target = confidence
    lo, hi = 0.0, 10.0
    for _ in range(100):
        mid = (lo + hi) / 2
        if math.erf(mid / math.sqrt(2)) < target:
            lo = mid
        else:
            hi = mid
    return (lo + hi) / 2
