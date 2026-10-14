"""Lab spec -> BNG Blaster JSON configuration.

Only the keys the Phase 0 runs need are generated; everything else keeps BNG
Blaster's defaults so that results stay comparable with other BNG Blaster
users. Key names follow the BNG Blaster configuration reference
(``interfaces.access``, ``sessions``, ``pppoe``, ``ppp``, ``dhcpv6``).
"""

from __future__ import annotations

from typing import Any

from .labspec import LabSpec


def render(
    spec: LabSpec,
    sessions: int,
    rate: int,
    session_time_s: float | None = None,
    max_outstanding: int | None = None,
) -> dict[str, Any]:
    """Builds a BNG Blaster configuration.

    ``sessions`` PPPoE sessions are started at ``rate`` per second.
    ``session_time_s`` makes each session disconnect after that long (keeps
    the population of a long TS-04 step bounded); None keeps sessions up
    until the harness requests teardown.
    """
    if sessions < 1 or rate < 1:
        raise ValueError("sessions and rate must be >= 1")
    access: dict[str, Any] = {"interface": spec.access.interface, "type": "pppoe"}
    if spec.access.outer_vlan:
        access["outer-vlan-min"], access["outer-vlan-max"] = spec.access.outer_vlan
    if spec.access.inner_vlan:
        access["inner-vlan-min"], access["inner-vlan-max"] = spec.access.inner_vlan

    interfaces: dict[str, Any] = {"access": [access]}
    if spec.is_selfloop:
        # BNG Blaster's A10NSP interface answers PPPoE itself: a loop-back
        # "BNG" for validating the harness without a DUT.
        interfaces["a10nsp"] = [{"interface": spec.selfloop.interface}]

    mo = max_outstanding if max_outstanding is not None else max(spec.blaster.max_outstanding, rate * 2)
    pppoe: dict[str, Any] = {
        "discovery-timeout": spec.blaster.discovery_timeout_s,
        "discovery-retry": spec.blaster.discovery_retry,
    }
    if session_time_s:
        pppoe["session-time"] = max(1, int(round(session_time_s)))

    return {
        "interfaces": interfaces,
        "sessions": {"count": sessions, "start-rate": rate, "stop-rate": max(rate, 1), "max-outstanding": mo},
        "pppoe": pppoe,
        "ppp": {
            "mru": spec.ppp.mru,
            "authentication": {
                "username": spec.ppp.username,
                "password": spec.ppp.password,
                "protocol": spec.ppp.auth.upper(),
            },
            "ipcp": {"enable": spec.ppp.ipcp},
            "ip6cp": {"enable": spec.ppp.ip6cp},
        },
        "dhcpv6": {"enable": spec.ppp.dhcpv6_pd},
    }
