"""Phase 0 BNG Blaster scenarios: TS-01 and TS-04 equivalents, N-session baseline.

These record the *incumbent's* view of each DUT before bngload exists, as the
Phase 0 acceptance criteria require ("BNG Blaster TS-01/TS-04-equivalent
numbers recorded per DUT"), and they are the yardstick for the "build or
extend BNG Blaster" decision (open question 8).

Equivalence to the design's TS-01/TS-04, and where it stops
---------------------------------------------------------------
* BNG Blaster reports one ``setup-time-ms`` per run, not per session. TS-01
  therefore runs **one session per BNG Blaster run**, repeated
  ``iterations`` times, which yields a per-session establishment-time
  distribution. Phase latencies (discovery, LCP, auth, NCP) are not
  available.
* TS-04 searches the offered setup rate. Each step offers ``rate x step_s``
  sessions and keeps them up until the step ends (BNG Blaster's counters only
  report the current number of established sessions, so sessions are not
  torn down mid-step). A step passes when ≥
  ``success_ratio`` of sessions came up, the retransmission ratio stays under
  ``max_retransmission_ratio`` and the last session came up within
  ``t_est_s`` of its scheduled start (the aggregate stand-in for
  "establishment p99 ≤ T_est"). The found rate is then verified with
  ``ceil(verify_s / step_s)`` consecutive step-length runs, all of which must
  pass — the ETSI NFV-TST 009 search-with-verification practice the design
  cites, adapted to keep the population bounded.
* There is no generator health gate around BNG Blaster. Keep ``max_rate``
  at or below one third of BNG Blaster's own ceiling on the host (design 5.6).
"""

from __future__ import annotations

import math
import sys
import time
from dataclasses import asdict, dataclass, field
from pathlib import Path
from typing import Any, Callable

from . import blaster_config
from .blaster_report import BlasterReport
from .blaster_run import BlasterError, BlasterRun, blaster_version
from .envelope import FAIL, INFO, PASS, WARN, Envelope, Finding, worst_of
from .labspec import LabSpec
from .stats import bootstrap_percentile_ci, summarize, wilson


def _log(msg: str) -> None:
    print(msg, file=sys.stderr, flush=True)


@dataclass
class Trial:
    """One BNG Blaster run."""

    name: str
    sessions: int
    rate: int
    report: BlasterReport | None = None
    error: str = ""
    wall_s: float = 0.0
    returncode: int | None = None

    def to_dict(self) -> dict[str, Any]:
        d = {k: v for k, v in asdict(self).items() if k != "report"}
        d["report"] = self.report.to_dict() if self.report else None
        return d


def run_trial(
    spec: LabSpec,
    workdir: Path,
    name: str,
    sessions: int,
    rate: int,
    establish_deadline_s: float,
    hold_s: float,
    session_time_s: float | None = None,
) -> Trial:
    """Starts BNG Blaster, waits for establishment (or the deadline), holds,
    tears down gracefully and parses the report."""
    t = Trial(name=name, sessions=sessions, rate=rate)
    cfg = blaster_config.render(spec, sessions, rate, session_time_s=session_time_s)
    run = BlasterRun(spec.blaster.binary, cfg, workdir, name, logging=spec.blaster.logging)
    t0 = time.monotonic()
    try:
        with run:
            run.start()
            c = run.wait_established(sessions, establish_deadline_s)
            if c.established_max >= sessions and hold_s > 0:
                time.sleep(hold_s)
            t.returncode = run.stop(timeout_s=sessions / rate + 30)
        t.report = run.report()
    except BlasterError as e:
        t.error = str(e)
    t.wall_s = time.monotonic() - t0
    return t


# --------------------------------------------------------------------------
# TS-01
# --------------------------------------------------------------------------


def ts01(spec: LabSpec, workdir: Path, iterations: int | None = None, runner: Callable[..., Trial] = run_trial) -> Envelope:
    """TS-01 equivalent: ``iterations`` sequential one-session runs."""
    cfg = spec.ts01
    n = iterations or cfg.iterations
    env = Envelope(tool="bngload-lab ts01")
    trials: list[Trial] = []
    interrupted = False
    try:
        for i in range(n):
            t = runner(spec, workdir, f"ts01-{i:03d}", 1, 1, cfg.deadline_s, cfg.hold_s)
            trials.append(t)
            r = t.report
            _log(
                f"ts01 {i + 1}/{n}: "
                + (f"established in {r.setup_time_ms:.0f} ms, retx {r.retransmissions}" if r and r.setup_time_ms is not None else f"FAILED {t.error or 'not established'}")
            )
            if i + 1 < n:
                time.sleep(cfg.interval_s)
    except KeyboardInterrupt:
        interrupted = True
    data = ts01_analyse(spec, trials)
    data["interrupted"] = interrupted
    data["requested_iterations"] = n
    findings = [Finding(**f) for f in data["findings"]]
    verdict = worst_of(findings)
    s = data["setup_time_ms"]
    summary = f"{data['established']}/{len(trials)} established" + (
        f", setup p50 {s['p50']:.0f} ms p99 {s['p99']:.0f} ms" if s["count"] else ""
    )
    return env.finish(verdict, summary, data)


def ts01_analyse(spec: LabSpec, trials: list[Trial]) -> dict[str, Any]:
    times = [t.report.setup_time_ms for t in trials if t.report and t.report.setup_time_ms is not None]
    ok = len(times)
    retx = sum(t.report.retransmissions for t in trials if t.report)
    summary = summarize(times, "ms")
    p50 = bootstrap_percentile_ci(times, 50)
    p99 = bootstrap_percentile_ci(times, 99)
    limit_ms = spec.ts01.t_est_s / 10 * 1000
    fs = [
        Finding(
            "ts01.success",
            "100% of iterations establish",
            PASS if trials and ok == len(trials) else FAIL,
            f"{ok}/{len(trials)}",
            ref="TS-01",
        ),
        Finding("ts01.retransmissions", "No retransmissions", PASS if retx == 0 else FAIL, f"{retx} retransmission(s)", ref="TS-01"),
    ]
    if times:
        fs.append(
            Finding(
                "ts01.latency",
                "Establishment p99 <= T_est/10",
                PASS if summary.p99 <= limit_ms else FAIL,
                f"p99 {summary.p99:.0f} ms vs {limit_ms:.0f} ms (T_est {spec.ts01.t_est_s:g} s)",
                ref="TS-01",
            )
        )
    if spec.is_selfloop:
        fs.append(Finding("ts01.selfloop", "Measured against a real DUT", WARN, "self-loop run: validates the harness, not a DUT"))
    return {
        "dut": spec.describe_dut(),
        "spec": spec.path,
        "blaster_version": _safe_version(spec),
        "equivalence": "one BNG Blaster run per iteration; setup-time-ms of a 1-session run is that session's establishment time",
        "iterations": [t.to_dict() for t in trials],
        "established": ok,
        "success": wilson(ok, len(trials)).to_dict() if trials else None,
        "setup_time_ms": summary.to_dict(),
        "setup_time_ms_p50_ci": p50.to_dict() if p50 else None,
        "setup_time_ms_p99_ci": p99.to_dict() if p99 else None,
        "retransmissions": retx,
        "findings": [f.to_dict() for f in fs],
    }


# --------------------------------------------------------------------------
# TS-04
# --------------------------------------------------------------------------


@dataclass
class Step:
    rate: int
    duration_s: float
    sessions: int
    passed: bool
    reasons: list[str] = field(default_factory=list)
    tail_lag_s: float | None = None
    kind: str = "search"  # search | verify
    trial: dict[str, Any] | None = None

    def to_dict(self) -> dict[str, Any]:
        return asdict(self)


def evaluate_step(rate: int, sessions: int, report: BlasterReport | None, error: str, spec: LabSpec) -> tuple[bool, list[str], float | None]:
    """Applies the TS-04 passing-step definition to one BNG Blaster report."""
    c = spec.ts04
    if report is None:
        return False, [error or "no report"], None
    reasons = []
    if report.success_ratio < c.success_ratio:
        reasons.append(f"{report.established}/{sessions} established ({report.success_ratio:.4f} < {c.success_ratio})")
    if report.retransmission_ratio > c.max_retransmission_ratio:
        reasons.append(f"retransmission ratio {report.retransmission_ratio:.4f} > {c.max_retransmission_ratio}")
    tail = None
    if report.setup_time_ms is not None:
        tail = report.setup_time_ms / 1000 - (sessions - 1) / rate
        if tail > c.t_est_s:
            reasons.append(f"last session {tail:.2f} s behind schedule > T_est {c.t_est_s:g} s")
    return not reasons, reasons, tail


@dataclass
class SearchResult:
    steps: list[Step]
    r_max: int | None
    limited_by: str  # "dut", "generator-bound", "dut-below-min"
    bracket: tuple[int, int] | None

    def to_dict(self) -> dict[str, Any]:
        return {
            "steps": [s.to_dict() for s in self.steps],
            "r_max": self.r_max,
            "limited_by": self.limited_by,
            "bracket": list(self.bracket) if self.bracket else None,
        }


def search(trial: Callable[[int, float, str], Step], min_rate: int, max_rate: int, precision: float, step_s: float, verify_s: float) -> SearchResult:
    """Binary search for the highest passing rate, then verification.

    ``trial(rate, duration_s, kind)`` runs one step. The search starts at
    ``max_rate`` (if that passes the DUT is faster than the generator bound)
    and ``min_rate`` (if that fails there is nothing to search), then halves
    the bracket until its relative width is ≤ ``precision``. The result is
    verified with one ``verify_s`` run; if verification fails the rate is
    lowered with exponential back-off (``precision``, then 2x, 4x and 8x
    that) so a DUT that only fails long runs still gets a verified,
    conservative result.
    """
    steps: list[Step] = []

    def run(rate: int, duration: float, kind: str = "search") -> Step:
        s = trial(rate, duration, kind)
        steps.append(s)
        return s

    limited_by = "dut"
    if run(max_rate, step_s).passed:
        best, limited_by, bracket = max_rate, "generator-bound", (max_rate, max_rate)
    elif not run(min_rate, step_s).passed:
        return SearchResult(steps, None, "dut-below-min", (0, min_rate))
    else:
        lo, hi = min_rate, max_rate
        while (hi - lo) / hi > precision:
            mid = (lo + hi) // 2
            if mid in (lo, hi):
                break
            if run(mid, step_s).passed:
                lo = mid
            else:
                hi = mid
        best, bracket = lo, (lo, hi)

    candidate, back_off = best, precision
    for _ in range(5):
        if run(candidate, verify_s, "verify").passed:
            return SearchResult(steps, candidate, limited_by, bracket)
        if candidate <= min_rate:
            break
        candidate = max(min_rate, int(candidate * (1 - back_off)))
        back_off = min(2 * back_off, 0.5)
        limited_by = "dut"
    return SearchResult(steps, None, "verification-failed", bracket)


def ts04(spec: LabSpec, workdir: Path, runner: Callable[..., Trial] = run_trial, quick: bool = False) -> Envelope:
    """TS-04 equivalent: maximum setup rate with verification."""
    c = spec.ts04
    env = Envelope(tool="bngload-lab ts04")
    step_s = min(c.step_s, 10.0) if quick else c.step_s
    verify_s = min(c.verify_s, 2 * step_s) if quick else c.verify_s
    counter = {"n": 0}

    def one_run(rate: int, duration: float, kind: str) -> tuple[bool, list[str], float | None, int, dict[str, Any]]:
        counter["n"] += 1
        sessions = max(1, int(round(rate * duration)))
        if c.max_sessions:
            sessions = min(sessions, c.max_sessions)
        t = runner(spec, workdir, f"ts04-{counter['n']:03d}-{kind}-{rate}", sessions, rate, sessions / rate + c.deadline_s, 0.0)
        ok, reasons, tail = evaluate_step(rate, sessions, t.report, t.error, spec)
        _log(f"ts04 {kind} {rate}/s, {sessions} sessions: " + ("pass" if ok else "fail: " + "; ".join(reasons)))
        return ok, reasons, tail, sessions, t.to_dict()

    def trial(rate: int, duration: float, kind: str) -> Step:
        if kind != "verify":
            ok, reasons, tail, sessions, tr = one_run(rate, duration, kind)
            return Step(rate, duration, sessions, ok, reasons, tail, kind, tr)
        cycles = max(1, math.ceil(duration / step_s - 1e-9))
        runs, tails, total = [], [], 0
        for i in range(cycles):
            ok, reasons, tail, sessions, tr = one_run(rate, step_s, "verify")
            runs.append(tr)
            total += sessions
            if tail is not None:
                tails.append(tail)
            if not ok:
                return Step(rate, duration, total, False, [f"cycle {i + 1}/{cycles}: " + r for r in reasons], max(tails, default=None), kind, {"cycles": runs})
        return Step(rate, duration, total, True, [], max(tails, default=None), kind, {"cycles": runs})

    interrupted = False
    try:
        result = search(trial, c.min_rate, c.max_rate, c.precision, step_s, verify_s)
    except KeyboardInterrupt:
        interrupted = True
        result = SearchResult([], None, "interrupted", None)

    fs = []
    if result.r_max is None:
        fs.append(Finding("ts04.r_max", "Maximum setup rate found and verified", FAIL, f"no verified passing rate ({result.limited_by})", ref="TS-04"))
    elif result.limited_by == "generator-bound":
        fs.append(
            Finding(
                "ts04.r_max",
                "Maximum setup rate found and verified",
                WARN,
                f"R_max ≥ {result.r_max}/s: the DUT passed the generator bound (ts04.max_rate)",
                ref="TS-04",
                remedy="raise ts04.max_rate if BNG Blaster's ceiling allows (stay ≤ 1/3 of it)",
            )
        )
    else:
        fs.append(Finding("ts04.r_max", "Maximum setup rate found and verified", INFO, f"R_max = {result.r_max}/s", ref="TS-04"))
    if quick:
        fs.append(Finding("ts04.quick", "Full step and verification durations", WARN, f"quick mode: {step_s:g} s steps, {verify_s:g} s verification"))
    if spec.is_selfloop:
        fs.append(Finding("ts04.selfloop", "Measured against a real DUT", WARN, "self-loop run: validates the harness, not a DUT"))
    data = {
        "dut": spec.describe_dut(),
        "spec": spec.path,
        "blaster_version": _safe_version(spec),
        "parameters": {**asdict(c), "step_s": step_s, "verify_s": verify_s, "quick": quick},
        "equivalence": "aggregate BNG Blaster counters; a step offers rate x step_s sessions and passes on success ratio, "
        "retransmission ratio and tail lag <= T_est; verification = consecutive step-length runs",
        **result.to_dict(),
        "interrupted": interrupted,
        "findings": [f.to_dict() for f in fs],
    }
    summary = f"R_max {result.r_max}/s ({result.limited_by})" if result.r_max else f"no R_max ({result.limited_by})"
    return env.finish(worst_of(fs), summary, data)


# --------------------------------------------------------------------------
# N-session baseline
# --------------------------------------------------------------------------


def baseline(spec: LabSpec, workdir: Path, runner: Callable[..., Trial] = run_trial) -> Envelope:
    """One run of ``baseline.sessions`` sessions at ``baseline.rate``."""
    b = spec.baseline
    env = Envelope(tool="bngload-lab baseline")
    deadline = (b.sessions - 1) / b.rate + b.deadline_s
    t = runner(spec, workdir, f"baseline-{b.sessions}", b.sessions, b.rate, deadline, b.hold_s)
    r = t.report
    fs = [
        Finding(
            "baseline.established",
            "All sessions established",
            PASS if r and r.established == b.sessions else FAIL,
            f"{r.established if r else 0}/{b.sessions}" + (f" ({t.error})" if t.error else ""),
        )
    ]
    if r and r.retransmissions:
        fs.append(Finding("baseline.retransmissions", "No retransmissions", WARN, f"{r.retransmissions} ({r.retransmission_ratio:.4f})"))
    data = {
        "dut": spec.describe_dut(),
        "spec": spec.path,
        "blaster_version": _safe_version(spec),
        "parameters": asdict(b),
        "trial": t.to_dict(),
        "findings": [f.to_dict() for f in fs],
    }
    summary = (
        f"{r.established}/{b.sessions} established in {r.setup_time_ms:.0f} ms ({r.setup_rate_cps:.1f}/s)"
        if r and r.setup_time_ms is not None and r.setup_rate_cps is not None
        else f"{r.established if r else 0}/{b.sessions} established"
    )
    return env.finish(worst_of(fs), summary, data)


def _safe_version(spec: LabSpec) -> str:
    try:
        return blaster_version(spec.blaster.binary)
    except BlasterError:
        return "unknown"
