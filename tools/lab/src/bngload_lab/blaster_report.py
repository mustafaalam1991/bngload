"""Parser for BNG Blaster's JSON report (``-J report.json``).

The report root is ``{"report": {...}}``. Fields used here:

* ``sessions`` — the session count (an int), or the per-session list when
  BNG Blaster runs with ``-j sessions``;
* ``sessions-established``, ``setup-time-ms`` (first start to last
  establishment) and ``setup-rate-cps``;
* ``access-interfaces[].protocol-stats`` — per-protocol TX/RX and timeout
  counters, from which retransmissions are derived.

BNG Blaster does not report a per-session establishment time, so TS-01 uses
one session per run (``setup-time-ms`` is then that session's time) and
TS-04 uses the aggregate rate. This is the "TS-01/TS-04-equivalent" of the
Phase 0 acceptance criteria.
"""

from __future__ import annotations

import json
from dataclasses import asdict, dataclass, field
from pathlib import Path
from typing import Any

# Timeout counters that each mean one retransmitted request.
_RETX_TIMEOUTS = (
    "lcp-request-timeout",
    "ipcp-request-timeout",
    "ip6cp-request-timeout",
    "pap-timeout",
    "chap-timeout",
    "dhcp-timeout",
    "dhcpv6-timeout",
    "icmpv6-rs-timeout",
)
# Control-plane frames sent by the client side.
_TX_CONTROL = ("tx-padi", "tx-padr", "tx-lcp", "tx-pap", "tx-chap", "tx-ipcp", "tx-ip6cp", "tx-dhcpv6", "tx-icmpv6")


@dataclass
class BlasterReport:
    version: str = ""
    sessions: int = 0
    established: int = 0
    flapped: int = 0
    setup_time_ms: float | None = None
    setup_rate_cps: float | None = None
    test_duration_s: float | None = None
    protocol: dict[str, int] = field(default_factory=dict)
    session_states: dict[str, int] = field(default_factory=dict)

    @property
    def success_ratio(self) -> float:
        return self.established / self.sessions if self.sessions else 0.0

    @property
    def retransmissions(self) -> int:
        """Timeout-driven retransmissions plus repeated PADI/PADR."""
        p = self.protocol
        n = sum(p.get(k, 0) for k in _RETX_TIMEOUTS)
        n += max(0, p.get("tx-padi", 0) - self.sessions)
        n += max(0, p.get("tx-padr", 0) - p.get("rx-pado", 0))
        return n

    @property
    def control_tx(self) -> int:
        return sum(self.protocol.get(k, 0) for k in _TX_CONTROL)

    @property
    def retransmission_ratio(self) -> float:
        tx = self.control_tx
        return self.retransmissions / tx if tx else 0.0

    def to_dict(self) -> dict[str, Any]:
        d = asdict(self)
        d.update(
            success_ratio=self.success_ratio,
            retransmissions=self.retransmissions,
            retransmission_ratio=self.retransmission_ratio,
            control_tx=self.control_tx,
        )
        return d


def parse(doc: dict[str, Any]) -> BlasterReport:
    r = doc.get("report", doc)
    if not isinstance(r, dict):
        raise ValueError("not a BNG Blaster report")
    out = BlasterReport(version=str(r.get("version", "")))
    sessions = r.get("sessions")
    if isinstance(sessions, list):
        out.sessions = len(sessions)
        for s in sessions:
            st = str(s.get("session-state", "unknown"))
            out.session_states[st] = out.session_states.get(st, 0) + 1
    elif isinstance(sessions, int):
        out.sessions = sessions
    else:
        out.sessions = int(r.get("sessions-pppoe", 0)) + int(r.get("sessions-ipoe", 0))
    out.established = int(r.get("sessions-established", 0))
    out.flapped = int(r.get("sessions-flapped", 0))
    if "setup-time-ms" in r:
        out.setup_time_ms = float(r["setup-time-ms"])
    if "setup-rate-cps" in r:
        out.setup_rate_cps = float(r["setup-rate-cps"])
    if "test-duration" in r:
        out.test_duration_s = float(r["test-duration"])
    for ai in r.get("access-interfaces", []) or []:
        for k, v in (ai.get("protocol-stats") or {}).items():
            if isinstance(v, int):
                out.protocol[k] = out.protocol.get(k, 0) + v
    # BNG Blaster reports a setup time even when not all sessions came up;
    # it is only meaningful when they all did.
    if out.established < out.sessions:
        out.setup_time_ms = None
        out.setup_rate_cps = None
    return out


def load(path: str | Path) -> BlasterReport:
    return parse(json.loads(Path(path).read_text()))
