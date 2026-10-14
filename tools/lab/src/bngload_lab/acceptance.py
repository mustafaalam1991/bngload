"""Phase 0 acceptance report.

Collects the ``bngload.report/v1`` envelopes written by every Phase 0 tool
and checks the four acceptance criteria of design section 15, Phase 0:

A1  hardware timestamps verified on RX and TX      (``bngload tstamp check``)
A2  chrony meets MR-2                              (``bngload clock check``)
A3  BNG Blaster TS-01/TS-04-equivalent numbers     (``bngload-lab ts01/ts04``)
    recorded per DUT
A4  every open question has an owner               (open-question register)

It also lists the Phase 0 deliverables with their evidence (host profile,
health check, netns/TUN kernel costs, I/O ceiling, N-session baselines) and
can merge the measured figures into the host profile (``data.measured``).
"""

from __future__ import annotations

import copy
from dataclasses import dataclass
from pathlib import Path
from typing import Any, Iterable

from . import envelope as E
from . import openq
from .envelope import FAIL, INFO, PASS, WARN, Finding

T_HEALTH = "bngload health"
T_HOST = "bngload host profile"
T_CLOCK = "bngload clock check"
T_TSTAMP = "bngload tstamp check"
T_KBENCH = "bngload kbench run"
T_AFPROBE = "afprobe"
T_TS01 = "bngload-lab ts01"
T_TS04 = "bngload-lab ts04"
T_BASELINE = "bngload-lab baseline"


def collect(paths: Iterable[str | Path]) -> list[dict[str, Any]]:
    """Loads every envelope found in the given files and directories."""
    out = []
    for p in paths:
        p = Path(p)
        files = sorted(p.rglob("*.json")) if p.is_dir() else [p]
        for f in files:
            env = E.load(f)
            if env is not None:
                out.append(env)
    return out


def _key(env: dict[str, Any]) -> Any:
    try:
        return E.parse_time(env.get("finished_at") or env.get("started_at") or "")
    except ValueError:
        return E.parse_time("1970-01-01T00:00:00Z")


def latest(envs: list[dict[str, Any]], tool: str, pred=lambda e: True) -> dict[str, Any] | None:
    cands = [e for e in envs if e.get("tool") == tool and pred(e)]
    return max(cands, key=_key) if cands else None


def _dut(env: dict[str, Any]) -> dict[str, Any]:
    return (env.get("data") or {}).get("dut") or {}


def _rel(env: dict[str, Any] | None) -> str:
    return env["_path"] if env else ""


@dataclass
class Item:
    id: str
    title: str
    status: str
    detail: str
    evidence: str = ""

    def to_dict(self) -> dict[str, Any]:
        return {"id": self.id, "title": self.title, "status": self.status, "detail": self.detail, "evidence": self.evidence}

    def finding(self) -> Finding:
        return Finding(self.id, self.title, self.status, self.detail, ref="Phase 0 acceptance")


def criterion_tstamp(envs: list[dict[str, Any]]) -> Item:
    verified = latest(envs, T_TSTAMP, lambda e: bool((e.get("data") or {}).get("hardware_rx_tx_verified")))
    if verified:
        d = verified["data"]
        hw = d.get("hardware_one_way_ns") or {}
        extra = f", HW one-way p50 {hw['p50']:.0f} ns" if hw.get("count") else " (ports on different PHCs)"
        return Item("A1", "Hardware timestamps verified on RX and TX", PASS, f"{d['tx_port']['interface']} → {d['rx_port']['interface']}{extra}", _rel(verified))
    last = latest(envs, T_TSTAMP)
    if not last:
        return Item("A1", "Hardware timestamps verified on RX and TX", FAIL, "no tstamp report: run `bngload tstamp check` on the test ports")
    d = last.get("data") or {}
    a = d.get("availability") or {}
    return Item(
        "A1",
        "Hardware timestamps verified on RX and TX",
        FAIL,
        f"mode {d.get('mode_used')}: HW stamps TX {a.get('tx_hardware', 0)}/{a.get('sent', 0)}, RX {a.get('rx_hardware', 0)}/{a.get('received', 0)}",
        _rel(last),
    )


def criterion_clock(envs: list[dict[str, Any]]) -> Item:
    last = latest(envs, T_CLOCK)
    if not last:
        return Item("A2", "chrony meets MR-2", FAIL, "no clock report: run `bngload clock check`")
    v = last.get("verdict", FAIL)
    status = {PASS: PASS, WARN: WARN, INFO: WARN}.get(v, FAIL)
    return Item("A2", "chrony meets MR-2", status, last.get("summary", ""), _rel(last))


def dut_results(envs: list[dict[str, Any]]) -> dict[str, dict[str, Any]]:
    """Latest TS-01/TS-04/baseline envelope per DUT name."""
    out: dict[str, dict[str, Any]] = {}
    for tool, key in ((T_TS01, "ts01"), (T_TS04, "ts04"), (T_BASELINE, "baseline")):
        for e in envs:
            if e.get("tool") != tool:
                continue
            d = _dut(e)
            name = d.get("name") or "?"
            slot = out.setdefault(name, {"dut": d})
            if key not in slot or _key(e) > _key(slot[key]):
                slot[key] = e
    return out


def criterion_duts(envs: list[dict[str, Any]]) -> Item:
    duts = dut_results(envs)
    real = {n: r for n, r in duts.items() if r["dut"].get("mode") != "selfloop"}
    if not real:
        detail = "no DUT results"
        if duts:
            detail += f" (self-loop only: {', '.join(sorted(duts))} validates the harness, not a DUT)"
        return Item("A3", "BNG Blaster TS-01/TS-04-equivalent numbers recorded per DUT", FAIL, detail)
    parts, status, evidence = [], PASS, []
    for name, r in sorted(real.items()):
        t1, t4 = r.get("ts01"), r.get("ts04")
        missing = [k for k, v in (("TS-01", t1), ("TS-04", t4)) if not v]
        if t1 and not (t1.get("data") or {}).get("established"):
            missing.append("TS-01 (no session established)")
        if t4 and (t4.get("data") or {}).get("r_max") is None:
            missing.append("TS-04 (no verified R_max)")
        if missing:
            status = FAIL
            parts.append(f"{name}: missing {', '.join(missing)}")
        else:
            s = t1["data"]["setup_time_ms"]
            parts.append(f"{name}: setup p50 {s['p50']:.0f} ms / p99 {s['p99']:.0f} ms, R_max {t4['data']['r_max']}/s")
        evidence += [_rel(x) for x in (t1, t4) if x]
    return Item("A3", "BNG Blaster TS-01/TS-04-equivalent numbers recorded per DUT", status, "; ".join(parts), ", ".join(evidence))


def criterion_openq(register: str | Path | None) -> tuple[Item, list[openq.Question]]:
    if not register:
        return Item("A4", "Every open question has an owner", FAIL, "no register given (--open-questions)"), []
    try:
        qs = openq.load(register)
    except (OSError, ValueError) as e:
        return Item("A4", "Every open question has an owner", FAIL, f"cannot read {register}: {e}", str(register)), []
    f = openq.check(qs)[0]
    return Item("A4", "Every open question has an owner", f.status, f.detail, str(register)), qs


def deliverables(envs: list[dict[str, Any]], signoff: str | None) -> list[Item]:
    items = []
    host = latest(envs, T_HOST)
    if host:
        ifs = (host.get("data") or {}).get("interfaces") or {}
        hw = sorted(n for n, i in ifs.items() if ((i or {}).get("timestamping") or {}).get("phc_index", -1) >= 0)
        items.append(
            Item(
                "D1",
                "Host profile with NIC timestamp support",
                PASS if hw else WARN,
                f"{len(ifs)} interface(s); hardware clock (PHC) on: {', '.join(hw) or 'none'}",
                _rel(host),
            )
        )
    else:
        items.append(Item("D1", "Host profile recorded", WARN, "run `bngload host profile`"))
    health = latest(envs, T_HEALTH)
    items.append(
        Item("D2", "Host health check", health.get("verdict", WARN) if health else WARN, health.get("summary", "") if health else "run `bngload health`", _rel(health))
    )
    kb = latest(envs, T_KBENCH)
    if kb:
        res = {r.get("scenario"): r for r in (kb.get("data") or {}).get("results") or []}
        have = [s for s in ("netns", "tierk") if s in res]
        detail = "; ".join(
            f"{s}: p50 {res[s]['create_us']['p50']:.0f} µs, {res[s].get('kernel_mem_kib_per_object', 0):.0f} KiB/object" for s in res
        )
        items.append(Item("D3", "netns/TUN kernel costs measured", PASS if len(have) == 2 else WARN, detail, _rel(kb)))
    else:
        items.append(Item("D3", "netns/TUN kernel costs measured", WARN, "run `bngload kbench run`"))
    af = latest(envs, T_AFPROBE)
    if af:
        c = (af.get("data") or {}).get("ceiling") or {}
        detail = (
            f"{c.get('pps', 0) / 1e6:.2f} Mpps ({c.get('tx_pps_per_core', 0) / 1e6:.2f} Mpps/core TX)" + (" lower bound" if c.get("lower_bound_only") else "")
            if c
            else af.get("summary", "")
        )
        virt = any((af.get("data") or {}).get(k, {}) and (af["data"][k] or {}).get("virtual") for k in ("tx_port", "rx_port"))
        items.append(Item("D4", "AF_PACKET I/O ceiling measured", WARN if virt or not c else PASS, detail + (" — virtual ports" if virt else ""), _rel(af)))
    else:
        items.append(Item("D4", "AF_PACKET I/O ceiling measured", WARN, "run `afprobe run`"))
    duts = dut_results(envs)
    base = [f"{n}: {r['baseline'].get('summary', '')}" for n, r in sorted(duts.items()) if r.get("baseline") and r["dut"].get("mode") != "selfloop"]
    loops = sorted(n for n, r in duts.items() if r.get("baseline") and r["dut"].get("mode") == "selfloop")
    hint = "run `bngload-lab baseline` against each DUT" + (f" (self-loop only so far: {', '.join(loops)})" if loops else "")
    items.append(Item("D5", "BNG Blaster N-session baseline per DUT", PASS if base else WARN, "; ".join(base) or hint))
    items.append(
        Item(
            "D6",
            "Requirements signed off; lab topology and DUT configs filed",
            PASS if signoff else WARN,
            f"signed off by {signoff}" if signoff else "manual: record with --signoff NAME once docs/phase0 checklist is complete",
        )
    )
    return items


def build(
    paths: Iterable[str | Path], register: str | Path | None = None, signoff: str | None = None
) -> tuple[E.Envelope, list[openq.Question], list[dict[str, Any]]]:
    """Returns the acceptance envelope, the open questions and the inputs."""
    envs = collect(paths)
    a4, qs = criterion_openq(register)
    criteria = [criterion_tstamp(envs), criterion_clock(envs), criterion_duts(envs), a4]
    dels = deliverables(envs, signoff)
    verdict = E.worst(*(c.status for c in criteria))
    passed = sum(c.status == PASS for c in criteria)
    duts = dut_results(envs)
    data = {
        "criteria": [c.to_dict() for c in criteria],
        "deliverables": [d.to_dict() for d in dels],
        "duts": {
            n: {
                "dut": r["dut"],
                **{k: {"summary": r[k].get("summary"), "verdict": r[k].get("verdict"), "path": _rel(r[k])} for k in ("ts01", "ts04", "baseline") if r.get(k)},
            }
            for n, r in sorted(duts.items())
        },
        "inputs": sorted({(e["tool"], e["_path"]) for e in envs}),
        "open_questions": openq.as_dicts(qs),
        "findings": [c.finding().to_dict() for c in criteria],
    }
    env = E.Envelope(tool="bngload-lab acceptance").finish(verdict, f"{passed}/4 acceptance criteria pass", data)
    return env, qs, envs


_ICON = {PASS: "PASS", WARN: "WARN", FAIL: "FAIL", INFO: "INFO"}


def to_markdown(env: E.Envelope, qs: list[openq.Question]) -> str:
    d = env.data
    host = env.to_dict()["host"]
    lines = [
        "# bngload Phase 0 acceptance report",
        "",
        f"Generated {env.finished_at} on `{host['hostname']}` (kernel {host['kernel']}). **Verdict: {env.verdict}** — {env.summary}.",
        "",
        "## Acceptance criteria (design section 15, Phase 0)",
        "",
        "| # | Criterion | Status | Detail | Evidence |",
        "| --- | --- | --- | --- | --- |",
    ]
    for c in d["criteria"]:
        lines.append(f"| {c['id']} | {c['title']} | {_ICON[c['status']]} | {c['detail']} | {c['evidence']} |")
    lines += ["", "## Deliverables", "", "| # | Deliverable | Status | Detail | Evidence |", "| --- | --- | --- | --- | --- |"]
    for c in d["deliverables"]:
        lines.append(f"| {c['id']} | {c['title']} | {_ICON.get(c['status'], c['status'])} | {c['detail']} | {c['evidence']} |")
    lines += ["", "## Results per DUT", ""]
    if d["duts"]:
        lines += ["| DUT | Mode | Firmware | TS-01 | TS-04 | Baseline |", "| --- | --- | --- | --- | --- | --- |"]
        for n, r in d["duts"].items():
            cell = lambda k: (r[k]["summary"] or "") if k in r else "—"  # noqa: E731
            lines.append(f"| {n} | {r['dut'].get('mode', '')} | {r['dut'].get('firmware', '')} | {cell('ts01')} | {cell('ts04')} | {cell('baseline')} |")
    else:
        lines.append("No BNG Blaster results yet.")
    lines += ["", "## Open questions (design 17.7)", "", openq.to_markdown(qs) if qs else "No register loaded.", "", "## Inputs", ""]
    lines += [f"- `{tool}`: {path}" for tool, path in d["inputs"]] or ["- none"]
    return "\n".join(lines) + "\n"


def merge_host_profile(envs: list[dict[str, Any]]) -> dict[str, Any] | None:
    """Returns the latest host-profile envelope with ``data.measured`` filled."""
    host = latest(envs, T_HOST)
    if not host:
        return None
    out = copy.deepcopy({k: v for k, v in host.items() if not k.startswith("_")})
    measured: dict[str, Any] = {}
    af = latest(envs, T_AFPROBE)
    if af and (af.get("data") or {}).get("ceiling"):
        measured["afprobe_ceiling"] = {**af["data"]["ceiling"], "source": _rel(af), "at": af.get("finished_at")}
    kb = latest(envs, T_KBENCH)
    if kb:
        measured["kbench"] = {
            r["scenario"]: {
                "create_us_p50": r["create_us"]["p50"],
                "create_us_p99": r["create_us"]["p99"],
                "kernel_mem_kib_per_object": r.get("kernel_mem_kib_per_object"),
                "teardown_ms": r.get("teardown_ms"),
                "count": r.get("created"),
            }
            for r in (kb.get("data") or {}).get("results") or []
        }
        measured["kbench_source"] = _rel(kb)
    ts = latest(envs, T_TSTAMP)
    if ts:
        d = ts.get("data") or {}
        measured["timestamping"] = {
            "mode_used": d.get("mode_used"),
            "hardware_rx_tx_verified": d.get("hardware_rx_tx_verified"),
            "software_one_way_ns_p50": (d.get("software_one_way_ns") or {}).get("p50"),
            "source": _rel(ts),
        }
    out["data"]["measured"] = measured
    return out
