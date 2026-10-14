"""Lab specification: one TOML file per DUT describing how to reach it.

See ``lab/blaster/lab.example.toml`` for an annotated example. Every key has a
default except ``dut.name`` and ``access.interface``. :func:`load` returns a
:class:`LabSpec` or raises :class:`SpecError` listing every problem found,
with its TOML key path, so a lab engineer can fix the file in one pass.
"""

from __future__ import annotations

import shutil
import sys
from dataclasses import asdict, dataclass, field, fields
from pathlib import Path
from typing import Any

if sys.version_info >= (3, 11):
    import tomllib
else:  # Ubuntu 22.04: apt install python3-tomli
    import tomli as tomllib


class SpecError(ValueError):
    """The lab specification is invalid; ``problems`` lists each issue."""

    def __init__(self, problems: list[str]):
        super().__init__("invalid lab specification:\n  - " + "\n  - ".join(problems))
        self.problems = problems


@dataclass
class Dut:
    name: str = ""
    vendor: str = ""
    firmware: str = ""
    # "external": a real BNG on the access port. "selfloop": BNG Blaster's
    # A10NSP interface plays the BNG on a second port or veth peer; used only
    # to validate the harness, never counted as DUT numbers.
    mode: str = "external"


@dataclass
class Access:
    interface: str = ""
    # [min, max]; empty for untagged / single-tagged.
    outer_vlan: list[int] = field(default_factory=list)
    inner_vlan: list[int] = field(default_factory=list)


@dataclass
class SelfLoop:
    interface: str = ""


@dataclass
class Ppp:
    auth: str = "pap"  # pap | chap
    username: str = "sub{session-global}@lab"
    password: str = "test"
    mru: int = 1492
    ipcp: bool = True
    ip6cp: bool = True
    dhcpv6_pd: bool = False


# Where BNG Blaster is looked for when blaster.binary is empty: the release
# .deb installs /usr/sbin/bngblaster, `make install` from source
# /usr/local/sbin/bngblaster.
BLASTER_CANDIDATES = ("/usr/sbin/bngblaster", "/usr/local/sbin/bngblaster")


def find_blaster() -> str:
    found = shutil.which("bngblaster")
    if found:
        return found
    for c in BLASTER_CANDIDATES:
        if Path(c).exists():
            return c
    return BLASTER_CANDIDATES[0]


@dataclass
class Blaster:
    # Empty = auto-detect (PATH, then /usr/sbin, then /usr/local/sbin).
    binary: str = ""
    max_outstanding: int = 800
    discovery_timeout_s: int = 5
    discovery_retry: int = 10
    # Extra BNG Blaster logging categories (-l), e.g. ["pppoe"].
    logging: list[str] = field(default_factory=list)


@dataclass
class Ts01:
    """TS-01 baseline, single session (design section 6)."""

    iterations: int = 100
    hold_s: float = 10.0
    interval_s: float = 1.0
    deadline_s: float = 30.0
    # Establishment p99 target T_est; TS-01 passes at p99 <= T_est / 10.
    t_est_s: float = 1.0


@dataclass
class Ts04:
    """TS-04 fixed-rate establishment / maximum setup rate (design section 6).

    Each step offers ``rate x step_s`` sessions at ``rate`` per second and
    keeps them up until the step ends, so the population peaks at
    ``rate x step_s`` (the design's hold time H = 30 s gives the same
    population at step_s = 30). BNG Blaster's counters only report the
    *current* number of established sessions, which is why sessions are not
    torn down mid-step.
    """

    min_rate: int = 10
    # Upper end of the search. Set it to 1/3 of the generator ceiling
    # (design 5.6): for BNG Blaster use its own measured maximum.
    max_rate: int = 1000
    step_s: float = 30.0
    precision: float = 0.02
    # Verification at the found rate: ceil(verify_s / step_s) consecutive
    # step-length runs, all of which must pass.
    verify_s: float = 600.0
    success_ratio: float = 0.999
    deadline_s: float = 30.0
    max_retransmission_ratio: float = 0.01
    # Establishment target T_est. BNG Blaster has no per-session times, so a
    # step passes when the last session came up within t_est_s of its
    # scheduled start ("tail lag"), the aggregate proxy for p99 <= T_est.
    t_est_s: float = 1.0
    # Licence or pool limit: a step never offers more sessions than this
    # (its duration is shortened instead). 0 = no limit.
    max_sessions: int = 0


@dataclass
class Baseline:
    """The N-session run from the Phase 0 suggested tests."""

    sessions: int = 1000
    rate: int = 100
    hold_s: float = 30.0
    deadline_s: float = 60.0


@dataclass
class LabSpec:
    lab: dict[str, Any] = field(default_factory=dict)
    dut: Dut = field(default_factory=Dut)
    access: Access = field(default_factory=Access)
    selfloop: SelfLoop = field(default_factory=SelfLoop)
    ppp: Ppp = field(default_factory=Ppp)
    blaster: Blaster = field(default_factory=Blaster)
    ts01: Ts01 = field(default_factory=Ts01)
    ts04: Ts04 = field(default_factory=Ts04)
    baseline: Baseline = field(default_factory=Baseline)
    path: str = ""

    @property
    def is_selfloop(self) -> bool:
        return self.dut.mode == "selfloop"

    def interfaces(self) -> list[str]:
        """Every interface BNG Blaster transmits on."""
        out = [self.access.interface]
        if self.is_selfloop:
            out.append(self.selfloop.interface)
        return out

    def describe_dut(self) -> dict[str, str]:
        return {**asdict(self.dut)}

    def to_dict(self) -> dict[str, Any]:
        d = asdict(self)
        d.pop("path", None)
        return d


_SECTIONS = {f.name: f for f in fields(LabSpec) if f.name not in ("lab", "path")}


def _fill(obj: Any, table: dict[str, Any], prefix: str, problems: list[str]) -> None:
    known = {f.name: f for f in fields(obj)}
    for key, value in table.items():
        if key not in known:
            problems.append(f"{prefix}.{key}: unknown key")
            continue
        current = getattr(obj, key)
        if isinstance(current, bool):
            ok = isinstance(value, bool)
        elif isinstance(current, float):
            ok = isinstance(value, (int, float)) and not isinstance(value, bool)
            value = float(value) if ok else value
        elif isinstance(current, int):
            ok = isinstance(value, int) and not isinstance(value, bool)
        elif isinstance(current, list):
            ok = isinstance(value, list)
        else:
            ok = isinstance(value, str)
        if not ok:
            problems.append(f"{prefix}.{key}: expected {type(current).__name__}, got {type(value).__name__}")
            continue
        setattr(obj, key, value)


def _check_range(name: str, r: list[int], problems: list[str]) -> None:
    if not r:
        return
    if len(r) != 2 or not all(isinstance(x, int) for x in r) or not (1 <= r[0] <= r[1] <= 4094):
        problems.append(f"{name}: expected [min, max] with 1 <= min <= max <= 4094, got {r}")


def parse(data: dict[str, Any], path: str = "") -> LabSpec:
    """Builds and validates a spec from a parsed TOML document."""
    spec = LabSpec(path=path)
    problems: list[str] = []
    for key, value in data.items():
        if key == "lab":
            if isinstance(value, dict):
                spec.lab = value
            else:
                problems.append("lab: expected a table")
        elif key in _SECTIONS:
            if not isinstance(value, dict):
                problems.append(f"{key}: expected a table")
            else:
                _fill(getattr(spec, key), value, key, problems)
        else:
            problems.append(f"{key}: unknown section")

    if not spec.dut.name:
        problems.append("dut.name: required (used to group results per DUT)")
    if spec.dut.mode not in ("external", "selfloop"):
        problems.append(f"dut.mode: must be 'external' or 'selfloop', got {spec.dut.mode!r}")
    if not spec.access.interface:
        problems.append("access.interface: required")
    if spec.is_selfloop:
        if not spec.selfloop.interface:
            problems.append("selfloop.interface: required when dut.mode = 'selfloop'")
        elif spec.selfloop.interface == spec.access.interface:
            problems.append("selfloop.interface: must differ from access.interface")
    _check_range("access.outer_vlan", spec.access.outer_vlan, problems)
    _check_range("access.inner_vlan", spec.access.inner_vlan, problems)
    if spec.access.inner_vlan and not spec.access.outer_vlan:
        problems.append("access.inner_vlan: needs access.outer_vlan (QinQ)")
    if spec.ppp.auth not in ("pap", "chap"):
        problems.append(f"ppp.auth: must be 'pap' or 'chap', got {spec.ppp.auth!r}")
    if not 576 <= spec.ppp.mru <= 1500:
        problems.append(f"ppp.mru: must be 576..1500, got {spec.ppp.mru}")
    if spec.ppp.dhcpv6_pd and not spec.ppp.ip6cp:
        problems.append("ppp.dhcpv6_pd: needs ppp.ip6cp = true")
    t1, t4, b = spec.ts01, spec.ts04, spec.baseline
    for name, value in (("ts01.iterations", t1.iterations), ("baseline.sessions", b.sessions), ("baseline.rate", b.rate)):
        if value < 1:
            problems.append(f"{name}: must be >= 1")
    for name, value in (
        ("ts01.deadline_s", t1.deadline_s),
        ("ts01.t_est_s", t1.t_est_s),
        ("ts04.step_s", t4.step_s),
        ("ts04.deadline_s", t4.deadline_s),
        ("ts04.t_est_s", t4.t_est_s),
        ("baseline.deadline_s", b.deadline_s),
    ):
        if value <= 0:
            problems.append(f"{name}: must be > 0")
    if not 1 <= t4.min_rate < t4.max_rate:
        problems.append(f"ts04: need 1 <= min_rate < max_rate, got {t4.min_rate}..{t4.max_rate}")
    if not 0 < t4.precision < 0.5:
        problems.append("ts04.precision: must be in (0, 0.5)")
    if t4.max_sessions < 0:
        problems.append("ts04.max_sessions: must be >= 0 (0 = no limit)")
    if t4.verify_s < t4.step_s:
        problems.append("ts04.verify_s: must be >= ts04.step_s")
    if not 0 < t4.success_ratio <= 1:
        problems.append("ts04.success_ratio: must be in (0, 1]")
    if problems:
        raise SpecError(problems)
    if not spec.blaster.binary:
        spec.blaster.binary = find_blaster()
    return spec


def load(path: str | Path) -> LabSpec:
    p = Path(path)
    try:
        data = tomllib.loads(p.read_text())
    except OSError as e:
        raise SpecError([f"{p}: {e.strerror or e}"]) from e
    except tomllib.TOMLDecodeError as e:
        raise SpecError([f"{p}: {e}"]) from e
    return parse(data, str(p))
