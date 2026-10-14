"""Safety interlocks (design section 13: SR-11 and the Phase 0 subset of SR-19).

The rules and wording are the same as the Go ``internal/safety`` package and
the Rust ``afprobe::safety`` module:

1. the lab marker ``/etc/bngload/lab-host`` must exist;
2. every interface BNG Blaster will use must be listed in
   ``/etc/bngload/interfaces.allow``;
3. a test interface must carry no IPv4 address and no global IPv6 address.

``BNGLOAD_LAB_MARKER``, ``BNGLOAD_INTERFACE_ALLOWLIST`` and
``BNGLOAD_ALLOWED_INTERFACES`` override the defaults for CI and tests.
"""

from __future__ import annotations

import ipaddress
import json
import os
import subprocess
from dataclasses import dataclass, field
from pathlib import Path
from typing import Callable, Iterable

DEFAULT_LAB_MARKER = "/etc/bngload/lab-host"
DEFAULT_ALLOWLIST = "/etc/bngload/interfaces.allow"


class NotPermitted(Exception):
    """Raised when an interlock refuses an action."""


@dataclass
class Guard:
    lab_marker: Path = Path(DEFAULT_LAB_MARKER)
    allowlist_path: Path = Path(DEFAULT_ALLOWLIST)
    allowed: list[str] = field(default_factory=list)

    @classmethod
    def from_env(cls, env: dict[str, str] | None = None) -> "Guard":
        env = dict(os.environ) if env is None else env
        g = cls()
        if env.get("BNGLOAD_LAB_MARKER"):
            g.lab_marker = Path(env["BNGLOAD_LAB_MARKER"])
        if env.get("BNGLOAD_INTERFACE_ALLOWLIST"):
            g.allowlist_path = Path(env["BNGLOAD_INTERFACE_ALLOWLIST"])
        if env.get("BNGLOAD_ALLOWED_INTERFACES"):
            g.allowed = [p.strip() for p in env["BNGLOAD_ALLOWED_INTERFACES"].split(",") if p.strip()]
        return g

    def lab_marker_fields(self) -> dict[str, str] | None:
        """``key=value`` fields of the marker, or None when it is missing."""
        try:
            text = self.lab_marker.read_text()
        except OSError:
            return None
        out = {}
        for line in text.splitlines():
            line = line.strip()
            if line and not line.startswith("#") and "=" in line:
                k, v = line.split("=", 1)
                out[k.strip()] = v.strip()
        return out

    def allowed_interfaces(self) -> list[str]:
        if self.allowed:
            return list(self.allowed)
        try:
            text = self.allowlist_path.read_text()
        except FileNotFoundError:
            return []
        names = (line.split("#", 1)[0].strip() for line in text.splitlines())
        return sorted(n for n in names if n)

    def require_tx(
        self, interfaces: Iterable[str], addresses: Callable[[str], list[str]] | None = None
    ) -> None:
        """Raises :class:`NotPermitted` listing every violated rule."""
        addresses = addresses or interface_addresses
        problems = []
        if self.lab_marker_fields() is None:
            problems.append(
                f"lab marker {self.lab_marker} is missing (create it on authorised lab hosts only: "
                f"sudo install -D -m 0644 /dev/null {self.lab_marker})"
            )
        try:
            allowed = self.allowed_interfaces()
        except OSError as e:
            problems.append(f"cannot read interface allowlist {self.allowlist_path}: {e}")
            allowed = None
        for ifname in interfaces:
            if allowed is not None and ifname not in allowed:
                problems.append(
                    f"interface {ifname} is not in the allowlist {self.allowlist_path} (add one interface name per line)"
                )
            try:
                routable = [a for a in addresses(ifname) if is_routable(a)]
            except (OSError, ValueError, subprocess.SubprocessError) as e:
                problems.append(f"cannot list addresses of {ifname}: {e}")
                continue
            if routable:
                problems.append(
                    f"interface {ifname} carries routable addresses [{' '.join(routable)}]; "
                    "a test port must have no IP address (SR-19)"
                )
        if problems:
            raise NotPermitted("refused by safety interlock:\n  - " + "\n  - ".join(problems))


def is_routable(addr: str) -> bool:
    """Any IPv4 address, or IPv6 other than loopback/link-local/multicast."""
    ip = ipaddress.ip_address(addr.split("%", 1)[0])
    if ip.version == 4:
        return True
    return not (ip.is_loopback or ip.is_link_local or ip.is_multicast or ip.is_unspecified)


def interface_addresses(ifname: str) -> list[str]:
    """Addresses on an interface, via ``ip -j addr show dev IF`` (iproute2)."""
    out = subprocess.run(
        ["ip", "-j", "addr", "show", "dev", ifname], capture_output=True, text=True, check=False, timeout=10
    )
    if out.returncode != 0:
        raise OSError(out.stderr.strip() or f"ip addr show dev {ifname} failed")
    links = json.loads(out.stdout or "[]")
    return [a["local"] for link in links for a in link.get("addr_info", []) if "local" in a]
