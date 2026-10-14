"""Real BNG Blaster runs over the blt0/blt1 veth pair (self-loop).

Needs root, BNG Blaster and the pair from scripts/lab-veth.sh, so it is
deselected by default:

    sudo -E python3 -m pytest -m blaster
"""

from __future__ import annotations

import os
from pathlib import Path

import pytest

from bngload_lab import labspec, scenarios

pytestmark = pytest.mark.blaster


@pytest.fixture
def spec(repo: Path) -> labspec.LabSpec:
    s = labspec.load(repo / "lab" / "blaster" / "selfloop-veth.toml")
    if os.geteuid() != 0 or not Path(s.blaster.binary).exists() or not Path("/sys/class/net/blt0").exists():
        pytest.skip("needs root, BNG Blaster and the blt0/blt1 veth pair")
    return s


def test_ts01_three_iterations(spec, tmp_path):
    spec.ts01.hold_s = 0.2
    spec.ts01.interval_s = 0
    env = scenarios.ts01(spec, tmp_path, iterations=3)
    d = env.to_dict()["data"]
    assert d["established"] == 3
    assert d["retransmissions"] == 0
    assert (tmp_path / "ts01-000.report.json").exists()


def test_baseline_200_sessions(spec, tmp_path):
    spec.baseline.sessions, spec.baseline.rate, spec.baseline.hold_s = 200, 200, 0.5
    env = scenarios.baseline(spec, tmp_path)
    assert env.verdict == "PASS", env.summary
