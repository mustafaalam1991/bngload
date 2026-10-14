"""BNG Blaster process runner and control-socket client.

One :class:`BlasterRun` is one BNG Blaster process: the harness writes the
configuration, starts ``bngblaster -C cfg -S sock -J report``, follows the
session counters over the control socket, requests a graceful teardown with
``test-stop`` (PADT and LCP Terminate for every session) and reads the JSON
report BNG Blaster writes on exit.

The control socket speaks JSON over a Unix stream socket: the client sends
``{"command": "...", "arguments": {...}}`` and reads the reply until EOF.
"""

from __future__ import annotations

import json
import signal
import socket
import subprocess
import time
from dataclasses import dataclass
from pathlib import Path
from typing import Any, Callable

from . import blaster_report


class BlasterError(RuntimeError):
    pass


def blaster_version(binary: str) -> str:
    try:
        out = subprocess.run([binary, "-v"], capture_output=True, text=True, timeout=10, check=False)
    except OSError as e:
        raise BlasterError(f"cannot run {binary}: {e}") from e
    for line in (out.stdout + out.stderr).splitlines():
        if line.lower().startswith("version:"):
            return line.split(":", 1)[1].strip()
    return "unknown"


def control(sock_path: str | Path, command: str, timeout: float = 5.0, **arguments: Any) -> dict[str, Any]:
    """Sends one control-socket command and returns the decoded reply."""
    msg: dict[str, Any] = {"command": command}
    if arguments:
        msg["arguments"] = arguments
    with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as s:
        s.settimeout(timeout)
        s.connect(str(sock_path))
        s.sendall(json.dumps(msg).encode())
        chunks = []
        while True:
            b = s.recv(65536)
            if not b:
                break
            chunks.append(b)
    reply = json.loads(b"".join(chunks) or b"{}")
    if reply.get("status") not in (None, "ok"):
        raise BlasterError(f"{command}: {reply}")
    return reply


@dataclass
class Counters:
    """Subset of the ``session-counters`` reply used by the harness."""

    sessions: int = 0
    established: int = 0
    established_max: int = 0
    outstanding: int = 0
    terminated: int = 0
    flapped: int = 0

    @classmethod
    def from_reply(cls, reply: dict[str, Any]) -> "Counters":
        c = reply.get("session-counters", {})
        return cls(
            sessions=int(c.get("sessions", 0)),
            established=int(c.get("sessions-established", 0)),
            established_max=int(c.get("sessions-established-max", 0)),
            outstanding=int(c.get("sessions-outstanding", 0)),
            terminated=int(c.get("sessions-terminated", 0)),
            flapped=int(c.get("sessions-flapped", 0)),
        )


class BlasterRun:
    """One BNG Blaster process."""

    def __init__(self, binary: str, config: dict[str, Any], workdir: str | Path, name: str, logging: list[str] | None = None):
        self.binary = binary
        self.config = config
        self.dir = Path(workdir)
        self.name = name
        self.logging = logging or []
        self.config_path = self.dir / f"{name}.config.json"
        self.report_path = self.dir / f"{name}.report.json"
        self.sock_path = self.dir / f"{name}.sock"
        self.log_path = self.dir / f"{name}.log"
        self.proc: subprocess.Popen[bytes] | None = None
        self.started_mono = 0.0

    def start(self, socket_timeout: float = 10.0) -> None:
        self.dir.mkdir(parents=True, exist_ok=True)
        for p in (self.report_path, self.sock_path):
            p.unlink(missing_ok=True)
        self.config_path.write_text(json.dumps(self.config, indent=2) + "\n")
        cmd = [self.binary, "-C", str(self.config_path), "-S", str(self.sock_path), "-J", str(self.report_path), "-b"]
        for cat in self.logging:
            cmd += ["-l", cat]
        log = open(self.log_path, "wb")
        try:
            self.proc = subprocess.Popen(cmd, stdout=log, stderr=subprocess.STDOUT, stdin=subprocess.DEVNULL)
        except OSError as e:
            raise BlasterError(f"cannot start {self.binary}: {e}") from e
        finally:
            log.close()
        self.started_mono = time.monotonic()
        deadline = time.monotonic() + socket_timeout
        while time.monotonic() < deadline:
            if self.proc.poll() is not None:
                raise BlasterError(f"bngblaster exited with {self.proc.returncode}; see {self.log_path}:\n{self._log_tail()}")
            if self.sock_path.exists():
                try:
                    self.counters()
                    return
                except OSError:
                    pass
            time.sleep(0.05)
        self.kill()
        raise BlasterError(f"control socket {self.sock_path} did not appear; see {self.log_path}")

    def command(self, command: str, **arguments: Any) -> dict[str, Any]:
        return control(self.sock_path, command, **arguments)

    def counters(self) -> Counters:
        return Counters.from_reply(self.command("session-counters"))

    def wait_established(
        self, target: int, deadline_s: float, poll_s: float = 0.1, on_poll: Callable[[Counters], None] | None = None
    ) -> Counters:
        """Polls until ``target`` sessions are established, the process
        exits, or ``deadline_s`` (from start) passes. Returns the last counters."""
        end = self.started_mono + deadline_s
        c = Counters()
        while True:
            if self.proc is not None and self.proc.poll() is not None:
                return c
            try:
                c = self.counters()
            except (OSError, ValueError, BlasterError):
                pass
            if on_poll:
                on_poll(c)
            if c.established_max >= target or time.monotonic() >= end:
                return c
            time.sleep(poll_s)

    def stop(self, timeout_s: float = 60.0) -> int:
        """Graceful teardown (``test-stop``), then SIGTERM, then SIGKILL."""
        if self.proc is None:
            return -1
        if self.proc.poll() is None:
            try:
                self.command("test-stop")
            except (OSError, ValueError, BlasterError):
                self.proc.send_signal(signal.SIGTERM)
            try:
                self.proc.wait(timeout=timeout_s)
            except subprocess.TimeoutExpired:
                self.proc.send_signal(signal.SIGTERM)
                try:
                    self.proc.wait(timeout=10)
                except subprocess.TimeoutExpired:
                    self.kill()
        return self.proc.returncode if self.proc.returncode is not None else -1

    def kill(self) -> None:
        if self.proc is not None and self.proc.poll() is None:
            self.proc.kill()
            self.proc.wait(timeout=10)

    def report(self) -> blaster_report.BlasterReport:
        try:
            return blaster_report.load(self.report_path)
        except (OSError, ValueError) as e:
            raise BlasterError(f"no usable report at {self.report_path}: {e}; see {self.log_path}") from e

    def _log_tail(self, lines: int = 15) -> str:
        try:
            return "\n".join(self.log_path.read_text(errors="replace").splitlines()[-lines:])
        except OSError:
            return ""

    def __enter__(self) -> "BlasterRun":
        return self

    def __exit__(self, *exc: Any) -> None:
        if self.proc is not None and self.proc.poll() is None:
            self.stop(timeout_s=30)
