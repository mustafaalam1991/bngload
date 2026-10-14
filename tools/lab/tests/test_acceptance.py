"""Acceptance report, open questions, safety and envelope handling."""

from __future__ import annotations

import datetime as dt
import json
from pathlib import Path

import pytest

from bngload_lab import acceptance, envelope, openq
from bngload_lab.cli import main
from bngload_lab.safety import Guard, NotPermitted, is_routable


def env(tool, data, verdict="INFO", summary="", finished="2026-10-01T10:00:00.123456789Z"):
    return {
        "schema": envelope.SCHEMA,
        "tool": tool,
        "version": "test",
        "host": {"hostname": "h", "kernel": "k"},
        "started_at": finished,
        "finished_at": finished,
        "verdict": verdict,
        "summary": summary,
        "data": data,
    }


def write(dir_: Path, name: str, doc) -> Path:
    p = dir_ / name
    p.write_text(json.dumps(doc))
    return p


REGISTER = """
[[question]]
id = "OQ-01"
question = "a?"
owner = "ana"
[[question]]
id = "OQ-02"
question = "b?"
owner = "bo"
status = "answered"
answer = "yes"
"""


@pytest.fixture
def full_run(tmp_path: Path) -> Path:
    d = tmp_path / "runs"
    d.mkdir()
    write(d, "tstamp-sw.json", env("bngload tstamp check", {"hardware_rx_tx_verified": False}, finished="2026-10-02T00:00:00Z"))
    write(
        d,
        "tstamp-hw.json",
        env(
            "bngload tstamp check",
            {"hardware_rx_tx_verified": True, "tx_port": {"interface": "p0"}, "rx_port": {"interface": "p1"}, "hardware_one_way_ns": {"count": 200, "p50": 812}},
        ),
    )
    write(d, "clock.json", env("bngload clock check", {}, verdict="PASS", summary="0 steps"))
    dut = {"name": "bng-1", "mode": "external", "firmware": "1.0"}
    write(d, "ts01-bng-1.json", env("bngload-lab ts01", {"dut": dut, "established": 100, "setup_time_ms": {"p50": 41, "p99": 77}}, summary="100/100"))
    write(d, "ts04-bng-1.json", env("bngload-lab ts04", {"dut": dut, "r_max": 812}, summary="R_max 812/s (dut)"))
    write(d, "ts04-loop.json", env("bngload-lab ts04", {"dut": {"name": "loop", "mode": "selfloop"}, "r_max": 9999}))
    write(d, "host.json", env("bngload host profile", {"interfaces": {"p0": {"timestamping": {"phc_index": 0}}}, "measured": {}}))
    write(d, "kbench.json", env("bngload kbench run", {"results": [{"scenario": "netns", "create_us": {"p50": 160, "p99": 300}, "created": 100}, {"scenario": "tierk", "create_us": {"p50": 340, "p99": 900}, "created": 100}]}))
    write(d, "afprobe.json", env("afprobe", {"ceiling": {"pps": 2.1e6, "tx_pps_per_core": 1.4e6}, "tx_port": {"virtual": False}, "rx_port": {"virtual": False}}))
    write(d, "not-an-envelope.json", {"report": {"sessions": 1}})
    (tmp_path / "oq.toml").write_text(REGISTER)
    return tmp_path


def test_all_criteria_pass(full_run: Path):
    env_, qs, inputs = acceptance.build([full_run / "runs"], full_run / "oq.toml", signoff="lab lead")
    crit = {c["id"]: c for c in env_.data["criteria"]}
    assert {k: c["status"] for k, c in crit.items()} == {"A1": "PASS", "A2": "PASS", "A3": "PASS", "A4": "PASS"}
    assert "812 ns" in crit["A1"]["detail"] and crit["A1"]["evidence"].endswith("tstamp-hw.json")
    assert "bng-1" in crit["A3"]["detail"] and "loop" not in crit["A3"]["detail"]
    assert env_.verdict == "PASS" and len(qs) == 2
    assert not any(e["_path"].endswith("not-an-envelope.json") for e in inputs)
    md = acceptance.to_markdown(env_, qs)
    assert "| A3 |" in md and "bng-1" in md
    merged = acceptance.merge_host_profile(inputs)
    assert merged["data"]["measured"]["afprobe_ceiling"]["pps"] == 2.1e6
    assert merged["data"]["measured"]["kbench"]["netns"]["create_us_p50"] == 160


def test_missing_evidence_fails(tmp_path: Path):
    (tmp_path / "oq.toml").write_text('[[question]]\nid = "OQ-01"\nquestion = "q"\n')
    env_, _, _ = acceptance.build([tmp_path], tmp_path / "oq.toml")
    assert all(c["status"] == "FAIL" for c in env_.data["criteria"])
    assert env_.summary == "0/4 acceptance criteria pass"


def test_selfloop_only_does_not_count(tmp_path: Path):
    write(tmp_path, "t.json", env("bngload-lab ts01", {"dut": {"name": "loop", "mode": "selfloop"}, "established": 1, "setup_time_ms": {"p50": 1, "p99": 1}}))
    item = acceptance.criterion_duts(acceptance.collect([tmp_path]))
    assert item.status == "FAIL" and "self-loop only" in item.detail


def test_open_question_checks():
    qs = [
        openq.Question("OQ-1", "a", owner="x", status="answered"),
        openq.Question("OQ-1", "b", owner="", status="open", due="2020-01-01"),
        openq.Question("OQ-3", "c", owner="y", status="maybe"),
    ]
    fs = {f.id: f for f in openq.check(qs, today=dt.date(2026, 10, 1))}
    assert fs["openq.owners"].status == "FAIL" and "OQ-1" in fs["openq.owners"].detail
    assert fs["openq.ids"].status == "FAIL"
    assert fs["openq.status"].status == "FAIL"
    assert fs["openq.answers"].status == "WARN"
    assert fs["openq.overdue"].status == "WARN"


def test_repo_register_has_twelve_questions(repo: Path):
    qs = openq.load(repo / "docs" / "phase0" / "open-questions.toml")
    assert [q.id for q in qs] == [f"OQ-{i:02d}" for i in range(1, 13)]


def test_atomic_write_ignores_planted_symlink(tmp_path: Path):
    victim = tmp_path / "victim"
    victim.write_text("keep")
    target = tmp_path / "r.json"
    (tmp_path / "r.json.tmp").symlink_to(victim)
    envelope.write_json_atomic(target, {"a": 1})
    assert victim.read_text() == "keep"
    assert json.loads(target.read_text()) == {"a": 1}
    assert (target.stat().st_mode & 0o777) == 0o644
    assert [p.name for p in tmp_path.iterdir() if p.name.startswith(".r.json.")] == []


def test_parse_time_accepts_nanoseconds():
    t = envelope.parse_time("2026-10-14T01:02:03.123456789Z")
    assert t.microsecond == 123456 and t.tzinfo is not None
    assert envelope.parse_time("2026-10-14T01:02:03Z").second == 3


def test_safety_rules(tmp_path: Path):
    g = Guard(lab_marker=tmp_path / "lab-host", allowlist_path=tmp_path / "allow")
    with pytest.raises(NotPermitted) as e:
        g.require_tx(["p0"], addresses=lambda _: [])
    assert "lab marker" in str(e.value) and "not in the allowlist" in str(e.value)
    (tmp_path / "lab-host").write_text("lab = x\n")
    (tmp_path / "allow").write_text("p0  # access\n")
    assert g.lab_marker_fields() == {"lab": "x"}
    g.require_tx(["p0"], addresses=lambda _: ["fe80::1%p0"])
    with pytest.raises(NotPermitted, match="SR-19"):
        g.require_tx(["p0"], addresses=lambda _: ["192.0.2.1"])
    g2 = Guard.from_env({"BNGLOAD_ALLOWED_INTERFACES": "a, b"})
    assert g2.allowed_interfaces() == ["a", "b"]
    assert is_routable("2001:db8::1") and not is_routable("ff02::1") and is_routable("10.0.0.1")


def test_cli_acceptance_and_exit_codes(full_run: Path, capsys):
    out = full_run / "out"
    code = main(["acceptance", str(full_run / "runs"), "--open-questions", str(full_run / "oq.toml"), "--out-dir", str(out), "--host-profile-out", str(out / "host.json")])
    assert code == 0
    assert (out / "acceptance.md").exists() and json.loads((out / "acceptance.json").read_text())["verdict"] == "PASS"
    assert json.loads((out / "host.json").read_text())["data"]["measured"]["kbench"]
    assert main(["open-questions", str(full_run / "oq.toml")]) == 0
    assert main(["check-spec", "-f", str(full_run / "missing.toml")]) == 2
    assert main(["bogus"]) == 2
