from __future__ import annotations

import sys
from pathlib import Path

import pytest

ROOT = Path(__file__).resolve().parents[1]
REPO = ROOT.parents[1]
sys.path.insert(0, str(ROOT / "src"))

FIXTURES = Path(__file__).parent / "fixtures"


@pytest.fixture
def fixtures() -> Path:
    return FIXTURES


@pytest.fixture
def repo() -> Path:
    return REPO


@pytest.fixture
def minimal_spec_toml() -> str:
    return '[dut]\nname = "dut-a"\n[access]\ninterface = "p0"\n'
