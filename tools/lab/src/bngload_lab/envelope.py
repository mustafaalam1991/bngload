"""The ``bngload.report/v1`` envelope shared by every Phase 0 tool.

Go (``bngload``), Rust (``afprobe``) and this package all write the same
wrapper so that the acceptance report can treat their results uniformly::

    {"schema": "bngload.report/v1", "tool": "...", "version": "...",
     "host": {"hostname": "...", "kernel": "..."},
     "started_at": "...", "finished_at": "...",
     "verdict": "INFO|PASS|WARN|FAIL", "summary": "...", "data": {...}}
"""

from __future__ import annotations

import datetime as _dt
import json
import os
import platform
import socket
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any, Iterable

from . import __version__

SCHEMA = "bngload.report/v1"

INFO, PASS, WARN, FAIL = "INFO", "PASS", "WARN", "FAIL"
_RANK = {INFO: 0, PASS: 1, WARN: 2, FAIL: 3}


def worst(*verdicts: str) -> str:
    """Most severe verdict (INFO when none are given)."""
    out = INFO
    for v in verdicts:
        if _RANK.get(v, 0) > _RANK[out]:
            out = v
    return out


@dataclass
class Finding:
    """One checked statement, as in the Go ``report.Finding``."""

    id: str
    title: str
    status: str
    detail: str = ""
    ref: str = ""
    remedy: str = ""

    def to_dict(self) -> dict[str, Any]:
        d: dict[str, Any] = {"id": self.id}
        if self.ref:
            d["ref"] = self.ref
        d["title"] = self.title
        d["status"] = self.status
        if self.detail:
            d["detail"] = self.detail
        if self.remedy:
            d["remedy"] = self.remedy
        return d


def worst_of(findings: Iterable[Finding]) -> str:
    return worst(*(f.status for f in findings))


def now_rfc3339() -> str:
    return _dt.datetime.now(_dt.timezone.utc).isoformat(timespec="microseconds").replace("+00:00", "Z")


def parse_time(s: str) -> _dt.datetime:
    """Parses the RFC 3339 times written by Go, Rust and Python tools.

    Go and Rust write nanoseconds, which ``fromisoformat`` (3.10) rejects, so
    the fraction is cut to microseconds first.
    """
    s = s.strip().replace("Z", "+00:00")
    if "." in s:
        head, rest = s.split(".", 1)
        n = 0
        while n < len(rest) and rest[n].isdigit():
            n += 1
        s = f"{head}.{rest[:n][:6].ljust(6, '0')}{rest[n:]}"
    t = _dt.datetime.fromisoformat(s)
    return t if t.tzinfo else t.replace(tzinfo=_dt.timezone.utc)


@dataclass
class Envelope:
    tool: str
    started_at: str = field(default_factory=now_rfc3339)
    finished_at: str = ""
    verdict: str = INFO
    summary: str = ""
    data: Any = None

    def finish(self, verdict: str, summary: str, data: Any) -> "Envelope":
        self.finished_at = now_rfc3339()
        self.verdict, self.summary, self.data = verdict, summary, data
        return self

    def to_dict(self) -> dict[str, Any]:
        return {
            "schema": SCHEMA,
            "tool": self.tool,
            "version": f"bngload-lab {__version__}",
            "host": {"hostname": socket.gethostname(), "kernel": platform.release()},
            "started_at": self.started_at,
            "finished_at": self.finished_at or now_rfc3339(),
            "verdict": self.verdict,
            "summary": self.summary,
            "data": self.data,
        }

    def write(self, path: str | os.PathLike[str]) -> Path:
        return write_json_atomic(path, self.to_dict())


def write_json_atomic(path: str | os.PathLike[str], obj: Any) -> Path:
    """Writes indented JSON via a temporary file and rename."""
    p = Path(path)
    p.parent.mkdir(parents=True, exist_ok=True)
    tmp = p.with_name(p.name + ".tmp")
    tmp.write_text(json.dumps(obj, indent=2, default=_json_default) + "\n")
    os.replace(tmp, p)
    return p


def _json_default(o: Any) -> Any:
    if hasattr(o, "to_dict"):
        return o.to_dict()
    if isinstance(o, Path):
        return str(o)
    raise TypeError(f"not JSON serialisable: {type(o).__name__}")


def load(path: str | os.PathLike[str]) -> dict[str, Any] | None:
    """Loads a file if it is a ``bngload.report/v1`` envelope, else None."""
    try:
        d = json.loads(Path(path).read_text())
    except (OSError, ValueError):
        return None
    if not isinstance(d, dict) or d.get("schema") != SCHEMA or "tool" not in d:
        return None
    d["_path"] = str(path)
    return d
