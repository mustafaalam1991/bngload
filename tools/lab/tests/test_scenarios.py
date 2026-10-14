"""Scenario logic with a fake BNG Blaster (no root, no processes)."""

from __future__ import annotations

from pathlib import Path

import pytest

from bngload_lab import labspec, scenarios
from bngload_lab.blaster_report import BlasterReport
from bngload_lab.scenarios import Step, Trial, evaluate_step, search


def spec(**ts04) -> labspec.LabSpec:
    return labspec.parse({"dut": {"name": "fake"}, "access": {"interface": "p0"}, "ts04": ts04})


def rep(sessions, established, setup_ms=None, **proto):
    r = BlasterReport(sessions=sessions, established=established, setup_time_ms=setup_ms, protocol=proto)
    if setup_ms is not None:
        r.setup_rate_cps = established / (setup_ms / 1000)
    return r


class FakeDut:
    """A DUT that sustains up to `capacity` setups/s."""

    def __init__(self, capacity: int):
        self.capacity = capacity
        self.calls: list[tuple[int, int]] = []

    def __call__(self, spec, workdir, name, sessions, rate, deadline, hold, session_time_s=None) -> Trial:
        self.calls.append((sessions, rate))
        if rate <= self.capacity:
            return Trial(name, sessions, rate, report=rep(sessions, sessions, setup_ms=(sessions - 1) / rate * 1000 + 50, **{"tx-padi": sessions}))
        # overloaded: sessions queue up, the tail falls behind schedule
        behind = (sessions / self.capacity - sessions / rate) * 1000
        return Trial(name, sessions, rate, report=rep(sessions, sessions, setup_ms=(sessions - 1) / rate * 1000 + behind, **{"tx-padi": sessions}))


def test_evaluate_step_rules():
    s = spec(t_est_s=1.0)
    assert evaluate_step(10, 100, rep(100, 100, setup_ms=9_900 + 200), "", s) == (True, [], pytest.approx(0.2))
    ok, reasons, _ = evaluate_step(10, 100, rep(100, 99), "", s)
    assert not ok and "99/100" in reasons[0]
    ok, reasons, _ = evaluate_step(10, 100, rep(100, 100, setup_ms=13_000), "", s)
    assert not ok and "behind schedule" in reasons[0]
    ok, reasons, _ = evaluate_step(10, 100, rep(100, 100, setup_ms=9_900, **{"tx-lcp": 100, "lcp-request-timeout": 5}), "", s)
    assert not ok and "retransmission" in reasons[0]
    assert evaluate_step(10, 100, None, "boom", s) == (False, ["boom"], None)


def test_search_converges_and_verifies():
    def trial(rate, duration, kind):
        return Step(rate, duration, int(rate * duration), rate <= 437, kind=kind)

    r = search(trial, 10, 1000, 0.02, 30, 600)
    assert r.limited_by == "dut"
    assert 437 * 0.98 <= r.r_max <= 437
    lo, hi = r.bracket
    assert (hi - lo) / hi <= 0.02
    assert r.steps[0].rate == 1000 and r.steps[1].rate == 10
    assert r.steps[-1].kind == "verify" and r.steps[-1].passed


def test_search_generator_bound_and_below_min():
    r = search(lambda rate, d, k: Step(rate, d, 1, True, kind=k), 10, 500, 0.02, 30, 600)
    assert (r.r_max, r.limited_by) == (500, "generator-bound")
    r = search(lambda rate, d, k: Step(rate, d, 1, False, kind=k), 10, 500, 0.02, 30, 600)
    assert (r.r_max, r.limited_by) == (None, "dut-below-min")


def test_search_backs_off_when_verification_fails():
    # passes short steps up to 300/s, but long verification only at <= 280/s
    def trial(rate, duration, kind):
        limit = 280 if kind == "verify" else 300
        return Step(rate, duration, 1, rate <= limit, kind=kind)

    r = search(trial, 10, 1000, 0.02, 30, 600)
    assert r.r_max is not None and r.r_max <= 280
    assert sum(s.kind == "verify" for s in r.steps) >= 2


def test_ts04_end_to_end_with_fake_dut(tmp_path: Path):
    dut = FakeDut(capacity=300)
    env = scenarios.ts04(spec(min_rate=10, max_rate=1000, step_s=10, verify_s=30, precision=0.05), tmp_path, runner=dut)
    d = env.to_dict()["data"]
    # the fake DUT falls > T_est (1 s) behind above 1.1 x capacity on 10 s steps
    assert 330 * 0.95 <= d["r_max"] <= 330
    verify = [s for s in d["steps"] if s["kind"] == "verify"][-1]
    assert len(verify["trial"]["cycles"]) == 3  # 30 s / 10 s
    assert env.verdict == "INFO"


def test_ts04_respects_session_cap(tmp_path: Path):
    dut = FakeDut(capacity=10_000)
    scenarios.ts04(spec(min_rate=10, max_rate=1000, step_s=60, verify_s=60, max_sessions=5000), tmp_path, runner=dut)
    assert max(s for s, _ in dut.calls) == 5000


def test_ts01_with_fake_runs(tmp_path: Path):
    times = iter([20, 22, 21, 25, None, 23])

    def runner(spec, workdir, name, sessions, rate, deadline, hold, session_time_s=None):
        t = next(times)
        return Trial(name, 1, 1, report=rep(1, 1 if t else 0, setup_ms=t, **{"tx-padi": 1}), error="" if t else "not established")

    s = spec()
    s.ts01.interval_s = 0
    env = scenarios.ts01(s, tmp_path, iterations=6, runner=runner)
    d = env.to_dict()["data"]
    assert d["established"] == 5
    assert d["setup_time_ms"]["p50"] == 22
    ids = {f["id"]: f["status"] for f in d["findings"]}
    assert ids["ts01.success"] == "FAIL" and ids["ts01.latency"] == "PASS"
    assert env.verdict == "FAIL"
    assert d["success"]["estimate"] == pytest.approx(5 / 6)
