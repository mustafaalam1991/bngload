"""``bngload-lab`` command line.

Commands
--------
``render-config``   print the BNG Blaster configuration a lab spec produces
``check-spec``      validate a lab spec
``ts01``            TS-01 equivalent (repeated one-session runs)
``ts04``            TS-04 equivalent (maximum setup rate with verification)
``baseline``        one N-session run
``open-questions``  check the open-question register
``acceptance``      build the Phase 0 acceptance report

Exit codes match the Go and Rust tools: 0 ok, 1 FAIL (or WARN with
``--strict``), 2 usage, 3 error.
"""

from __future__ import annotations

import argparse
import datetime as dt
import json
import os
import sys
from pathlib import Path

from . import __version__, acceptance, blaster_config, envelope, labspec, openq, scenarios
from .blaster_run import BlasterError
from .safety import Guard, NotPermitted

EXIT_OK, EXIT_FAIL, EXIT_USAGE, EXIT_ERROR = 0, 1, 2, 3


def _exit_for(verdict: str, strict: bool) -> int:
    if verdict == envelope.FAIL or (strict and verdict == envelope.WARN):
        return EXIT_FAIL
    return EXIT_OK


def _print_findings(data: dict) -> None:
    for f in data.get("findings", []):
        line = f"[{f['status']}] {f['title']}"
        if f.get("detail"):
            line += f": {f['detail']}"
        print(line, file=sys.stderr)
        if f.get("remedy") and f["status"] in (envelope.WARN, envelope.FAIL):
            print(f"       → {f['remedy']}", file=sys.stderr)


def _spec(path: str) -> labspec.LabSpec:
    return labspec.load(path)


def _preflight(spec: labspec.LabSpec) -> None:
    """Root, BNG Blaster present, safety interlock on every TX interface."""
    if os.geteuid() != 0:
        raise NotPermitted("BNG Blaster needs root (CAP_NET_RAW, CAP_NET_ADMIN): run with sudo")
    if not Path(spec.blaster.binary).exists():
        raise BlasterError(f"BNG Blaster not found at {spec.blaster.binary} (scripts/install-bngblaster.sh, or set blaster.binary)")
    Guard.from_env().require_tx(spec.interfaces())


def _out_path(args: argparse.Namespace, spec: labspec.LabSpec, scenario: str) -> Path:
    if args.output:
        return Path(args.output)
    return Path(args.out_dir) / f"{scenario}-{spec.dut.name}.json"


def _workdir(args: argparse.Namespace, spec: labspec.LabSpec, scenario: str) -> Path:
    stamp = dt.datetime.now(dt.timezone.utc).strftime("%Y%m%dT%H%M%SZ")
    return Path(args.out_dir) / "blaster" / f"{scenario}-{spec.dut.name}-{stamp}"


def cmd_check_spec(args: argparse.Namespace) -> int:
    spec = _spec(args.spec)
    print(json.dumps(spec.to_dict(), indent=2))
    return EXIT_OK


def cmd_render(args: argparse.Namespace) -> int:
    spec = _spec(args.spec)
    cfg = blaster_config.render(spec, args.sessions, args.rate, session_time_s=args.session_time)
    print(json.dumps(cfg, indent=2))
    return EXIT_OK


def _scenario(args: argparse.Namespace, name: str, fn) -> int:
    spec = _spec(args.spec)
    _preflight(spec)
    workdir = _workdir(args, spec, name)
    print(f"bngload-lab {name}: DUT {spec.dut.name} ({spec.dut.mode}), BNG Blaster files in {workdir}", file=sys.stderr)
    env = fn(spec, workdir)
    path = env.write(_out_path(args, spec, name))
    _print_findings(env.data)
    print(f"verdict {env.verdict}: {env.summary}\nreport written to {path}", file=sys.stderr)
    return _exit_for(env.verdict, args.strict)


def cmd_ts01(args: argparse.Namespace) -> int:
    return _scenario(args, "ts01", lambda s, w: scenarios.ts01(s, w, iterations=args.iterations))


def cmd_ts04(args: argparse.Namespace) -> int:
    return _scenario(args, "ts04", lambda s, w: scenarios.ts04(s, w, quick=args.quick))


def cmd_baseline(args: argparse.Namespace) -> int:
    def run(s: labspec.LabSpec, w: Path):
        if args.sessions:
            s.baseline.sessions = args.sessions
        if args.rate:
            s.baseline.rate = args.rate
        return scenarios.baseline(s, w)

    return _scenario(args, "baseline", run)


def cmd_openq(args: argparse.Namespace) -> int:
    env = openq.report(args.register)
    if args.output:
        env.write(args.output)
    if args.markdown:
        print(openq.to_markdown(openq.load(args.register)))
    _print_findings(env.data)
    print(f"verdict {env.verdict}: {env.summary}", file=sys.stderr)
    return _exit_for(env.verdict, args.strict)


def cmd_acceptance(args: argparse.Namespace) -> int:
    env, qs, inputs = acceptance.build(args.runs, args.open_questions, args.signoff)
    out = Path(args.out_dir)
    env.write(out / "acceptance.json")
    (out / "acceptance.md").write_text(acceptance.to_markdown(env, qs))
    if args.host_profile_out:
        merged = acceptance.merge_host_profile(inputs)
        if merged is None:
            print("no host profile found; skipping --host-profile-out", file=sys.stderr)
        else:
            envelope.write_json_atomic(args.host_profile_out, merged)
            print(f"host profile with measured figures written to {args.host_profile_out}", file=sys.stderr)
    for c in env.data["criteria"]:
        print(f"[{c['status']}] {c['id']} {c['title']}: {c['detail']}", file=sys.stderr)
    for c in env.data["deliverables"]:
        print(f"[{c['status']}] {c['id']} {c['title']}: {c['detail']}", file=sys.stderr)
    print(f"verdict {env.verdict}: {env.summary}\nreport written to {out / 'acceptance.md'}", file=sys.stderr)
    return _exit_for(env.verdict, args.strict)


def build_parser() -> argparse.ArgumentParser:
    p = argparse.ArgumentParser(prog="bngload-lab", description="bngload Phase 0 lab harness")
    p.add_argument("--version", action="version", version=f"bngload-lab {__version__}")
    sub = p.add_subparsers(dest="command", required=True)

    def common(sp: argparse.ArgumentParser, spec: bool = True) -> None:
        if spec:
            sp.add_argument("-f", "--spec", required=True, help="lab specification (TOML)")
        sp.add_argument("--out-dir", default="runs", help="directory for reports [runs]")
        sp.add_argument("-o", "--output", help="report path [OUT_DIR/<scenario>-<dut>.json]")
        sp.add_argument("--strict", action="store_true", help="exit 1 on WARN as well as FAIL")

    sp = sub.add_parser("check-spec", help="validate a lab spec and print it with defaults")
    sp.add_argument("-f", "--spec", required=True)
    sp.set_defaults(fn=cmd_check_spec)

    sp = sub.add_parser("render-config", help="print the BNG Blaster config for a spec")
    sp.add_argument("-f", "--spec", required=True)
    sp.add_argument("--sessions", type=int, default=1)
    sp.add_argument("--rate", type=int, default=1)
    sp.add_argument("--session-time", type=float, default=None)
    sp.set_defaults(fn=cmd_render)

    sp = sub.add_parser("ts01", help="TS-01 equivalent: repeated one-session runs")
    common(sp)
    sp.add_argument("--iterations", type=int, help="override ts01.iterations")
    sp.set_defaults(fn=cmd_ts01)

    sp = sub.add_parser("ts04", help="TS-04 equivalent: maximum setup rate")
    common(sp)
    sp.add_argument("--quick", action="store_true", help="10 s steps and 20 s verification (smoke test only)")
    sp.set_defaults(fn=cmd_ts04)

    sp = sub.add_parser("baseline", help="one N-session run")
    common(sp)
    sp.add_argument("--sessions", type=int)
    sp.add_argument("--rate", type=int)
    sp.set_defaults(fn=cmd_baseline)

    sp = sub.add_parser("open-questions", help="check the open-question register")
    sp.add_argument("register", help="open-questions.toml")
    sp.add_argument("-o", "--output", help="write the JSON report here")
    sp.add_argument("--markdown", action="store_true", help="print the register as a Markdown table")
    sp.add_argument("--strict", action="store_true")
    sp.set_defaults(fn=cmd_openq)

    sp = sub.add_parser("acceptance", help="build the Phase 0 acceptance report")
    sp.add_argument("runs", nargs="+", help="report files or directories (searched recursively)")
    sp.add_argument("--open-questions", help="open-question register (TOML)")
    sp.add_argument("--signoff", help="name of whoever signed off the Phase 0 requirements")
    sp.add_argument("--out-dir", default="runs", help="where acceptance.md/.json are written [runs]")
    sp.add_argument("--host-profile-out", help="also write the host profile with measured figures merged in")
    sp.add_argument("--strict", action="store_true")
    sp.set_defaults(fn=cmd_acceptance)
    return p


def main(argv: list[str] | None = None) -> int:
    parser = build_parser()
    try:
        args = parser.parse_args(argv)
    except SystemExit as e:
        return int(e.code or 0) and EXIT_USAGE
    try:
        return args.fn(args)
    except labspec.SpecError as e:
        print(f"bngload-lab: {e}", file=sys.stderr)
        return EXIT_USAGE
    except NotPermitted as e:
        print(f"bngload-lab: {e}", file=sys.stderr)
        return EXIT_FAIL
    except (BlasterError, OSError, ValueError) as e:
        print(f"bngload-lab: {e}", file=sys.stderr)
        return EXIT_ERROR


if __name__ == "__main__":
    sys.exit(main())
