# Contributing to bngload

Thanks for helping build bngload. This guide covers how to set up, the
rules every change must follow, and how changes get reviewed. Read the
[README](README.md) first for what the project is and the
[technical design](docs/design/bngload-technical-design.md) for why it is
built this way.

> **bngload is for authorised lab testing only.** It emulates thousands of
> subscribers against a BNG. Never run it, or a test you wrote for it, on a
> host or port that can reach production, and never weaken a safety check to
> make a test pass (see [Safety rules](#safety-rules)).

## Contents

- [Ways to contribute](#ways-to-contribute)
- [Development setup](#development-setup)
- [Repository map](#repository-map)
- [Building and testing](#building-and-testing)
- [Rules every change follows](#rules-every-change-follows)
- [Safety rules](#safety-rules)
- [Language-specific conventions](#language-specific-conventions)
- [Documentation and design changes](#documentation-and-design-changes)
- [Commits and pull requests](#commits-and-pull-requests)
- [Reporting bugs and security issues](#reporting-bugs-and-security-issues)
- [License](#license)

## Ways to contribute

- **Lab results and host profiles.** Phase 0 runs on real NICs and DUTs are
  the most valuable contribution right now: commit `hosts/<host>.json` and
  attach the run directory (or its `acceptance.md`) to the pull request.
- **DUT configurations.** Working configs for vendor BNGs under
  `lab/dut/<vendor>/`, with the firmware version in the file name and every
  secret removed.
- **Answers to open questions.** Owners, answers and evidence in
  [`docs/phase0/open-questions.toml`](docs/phase0/open-questions.toml).
- **Bug fixes and tests,** especially driver- or NIC-specific behaviour seen
  on hardware that CI cannot reproduce.
- **New features,** following the phase plan in design section 15. Open an
  issue first for anything larger than a fix, so the scope and phase can be
  agreed before you write code.

## Development setup

You need Linux (Ubuntu 22.04 or 24.04 is what CI and the scripts target),
root for the integration tests, and three toolchains:

| Toolchain | Version | Used for |
| --- | --- | --- |
| Go | ≥ 1.22 (`go.mod`) | `cmd/bngload`, `internal/` |
| Rust | stable, edition 2021, MSRV 1.77 | `engines/afprobe` |
| Python | ≥ 3.10 (+ `tomli` on 3.10) | `tools/lab` |

On Ubuntu, one script installs everything:

```sh
sudo scripts/install-deps-ubuntu.sh      # apt packages, Go tarball (checksum-verified), rustup
sudo scripts/install-bngblaster.sh       # only needed for the BNG Blaster tests and runs
pip install -e 'tools/lab[test]'         # pytest (and tomli on 3.10) for the Python tests
```

You do not need NICs or a DUT to develop. A veth pair stands in for both:

```sh
sudo scripts/lab-veth.sh up              # blt0 <-> blt1, no IP, MTU 1508
eval "$(scripts/lab-veth.sh env)"        # scratch lab marker and allowlist for the interlock
sudo scripts/lab-veth.sh down            # when you are done
```

The `env` line points the tools at a scratch lab marker and allowlist under
`/tmp`, so you never need to create `/etc/bngload/lab-host` on a laptop or
workstation. Do not create the real marker on a machine that is not a lab
generator.

## Repository map

| Path | Language | What lives there |
| --- | --- | --- |
| `cmd/bngload/` | Go | CLI entry point and command wiring only — no logic |
| `internal/` | Go | one package per concern: `cli`, `report`, `stats`, `safety`, `linux`, `nic`, `netlinkx`, `pktio/afpacket`, `clock`, `tstamp`, `kbench`, `sysinfo`, `health`, `hostprofile`, `version` |
| `test/integration/` | Go | root-only tests over veth (build tag `integration`) |
| `engines/afprobe/` | Rust | AF_PACKET I/O-ceiling probe (library + binary) |
| `tools/lab/` | Python | `bngload-lab` harness (`src/bngload_lab`, `tests/`) |
| `scripts/` | Bash | install, host-prep, lab-veth, phase0-run |
| `lab/` | config | topology, BNG Blaster lab specs, DUT and AAA configs |
| `hosts/` | JSON | measured host profiles, one per generator host |
| `docs/` | Markdown | design, Phase 0 runbook and checklists, ADRs |

Why three languages, and which goes where: [ADR-0001](docs/adr/0001-language-per-module.md).
New Go code goes into `internal/` following the layout in design 17.10.

## Building and testing

```sh
make                          # bin/bngload and engines/afprobe/target/release/afprobe
make test                     # Go vet + tests, cargo test, pytest — no root needed
make lint                     # go vet, cargo clippy -D warnings, shellcheck, pyflakes
sudo -E make test-integration # root: Go veth tests, afprobe veth tests, BNG Blaster self-loop
```

`test-integration` needs `scripts/lab-veth.sh up` and the `env` line above
for the BNG Blaster part; the Go and Rust tests create and remove their own
veth pairs (`bit*`, `afp*`).

For an end-to-end check of everything together:

```sh
sudo -E scripts/phase0-run.sh --tx-if blt0 --rx-if blt1 \
     --spec lab/blaster/selfloop-veth.toml --quick --out /tmp/p0-smoke
```

Every measurement step should exit 0. The steps that judge the lab —
typically `health`, `clock` (without chrony), `open-questions` and
`acceptance` — correctly report FAIL on a veth pair with no hardware clock
and no real DUT.

**CI** (`.github/workflows/ci.yml`) runs the unit tests for all three
languages and shellcheck on pushes to `main` and on every pull request, then the root
integration tests and the same `--quick` smoke run on a GitHub Ubuntu runner.
A pull request is mergeable only when CI is green.

### What CI cannot test

Hardware timestamps, NIC driver quirks and real DUT behaviour need lab
hardware. When you change `internal/nic`, `internal/tstamp`,
`internal/pktio/afpacket`, `engines/afprobe/src/ring.rs` or the BNG Blaster
runner, say in the pull request which NIC, driver, firmware and kernel you
tested on, and attach the relevant JSON report.

## Rules every change follows

### One report format

Every tool writes the same JSON envelope, implemented in
`internal/report` (Go), `engines/afprobe/src/report.rs` (Rust) and
`tools/lab/src/bngload_lab/envelope.py` (Python):

```json
{"schema": "bngload.report/v1", "tool": "…", "version": "…",
 "host": {"hostname": "…", "kernel": "…"},
 "started_at": "RFC 3339", "finished_at": "RFC 3339",
 "verdict": "INFO|PASS|WARN|FAIL", "summary": "one line", "data": {…}}
```

- Put raw measurements in `data` and a `findings` list in `data.findings`.
- A finding has an `id` (`tool.check`, e.g. `tstamp.hardware`), a `ref` to
  the design requirement it checks (`MR-2`, `SR-19`, `TS-04`, `2.1`), a
  `title` phrased as the expectation ("RX interrupt coalescing off"), a
  `status`, a `detail` with the measured value, and a `remedy` for WARN and
  FAIL.
- The verdict is the worst finding. `INFO` is for measurements that have no
  pass/fail threshold.
- Adding fields is fine; renaming or removing a field, or changing its unit,
  needs a schema version bump (`bngload.report/v2`) and an update to
  `bngload_lab.acceptance`, which reads these files.
- Write report files atomically (temporary file + rename); the helpers do
  this already.

### Same behaviour in every language

Some rules exist in Go, Rust and Python. A change to one is a change to all
three, with the same reference values in each language's tests:

| Rule | Go | Rust | Python |
| --- | --- | --- | --- |
| Safety interlock | `internal/safety` | `afprobe::safety` | `bngload_lab.safety` |
| Percentiles (type 7) and summary keys | `internal/stats` | `afprobe::stats` | `bngload_lab.stats` |
| Report envelope | `internal/report` | `afprobe::report` | `bngload_lab.envelope` |
| Exit codes | `internal/cli` | `src/main.rs` | `bngload_lab.cli` |

Exit codes everywhere: `0` ok, `1` FAIL (or WARN with `--strict`), `2` usage
error, `3` runtime error.

### Measurement correctness

These come from the design's measurement requirements and are checked in
review:

- **MR-1:** compute an interval only from two stamps in the same clock
  domain. Hardware stamps from different PHCs are never subtracted into a
  latency; report jitter instead.
- **MR-4:** keep timestamps as 64-bit integer nanoseconds; convert units only
  at the edge (JSON, display).
- Never take timing from user space when a kernel or NIC stamp exists;
  user-space times are reported only as the error the stamps remove.
- Statistics are reproducible: no unseeded randomness (the bootstrap uses a
  fixed seed), and small samples report their interval rather than hiding it.
- If you add a metric, state its unit in the field name or in the `unit`
  key of its summary.

### Dependencies

Phase 0 keeps the run-time dependency set minimal so that every tool builds
and runs on an air-gapped lab host:

- **Go:** standard library only ([ADR-0003](docs/adr/0003-stdlib-only-go.md)).
  `go.sum` stays empty. Adding a module needs a new ADR, and it must be
  vendored (`go mod vendor`).
- **Rust:** `libc` only, pinned by the committed `Cargo.lock`; build with
  `--locked`.
- **Python:** standard library at run time (plus `tomli` on 3.10). Test-only
  tools (`pytest`, `pyflakes`) are fine.

A pull request that adds a dependency explains what it replaces, its licence
(which must be compatible with GPLv3, see [License](#license)), and how it is
vendored for offline builds.

## Safety rules

Every tool that transmits frames, or changes kernel state on the host, must
pass the interlock before it opens a socket or creates an object:

| Situation | Go | Rust | Python |
| --- | --- | --- | --- |
| Transmits on an interface | `safety.FromEnv().RequireTX(ifaces...)` | `Guard::from_env().require_tx(&ifaces, sys::addresses_of)` | `Guard.from_env().require_tx(ifaces)` |
| Changes kernel state only (namespaces, links) | `safety.FromEnv().RequireLabHost()` | — | — |

The interlock refuses unless the lab marker `/etc/bngload/lab-host` exists,
every interface is in `/etc/bngload/interfaces.allow`, and no interface
carries an IPv4 or global IPv6 address (design SR-11 and the Phase 0 part of
SR-19). In addition:

- **Rate caps.** New transmit paths start with a conservative cap (the design
  SR-14 defaults: 20 kpps total, 500 PADI/s, 100 setups/s) that only an
  explicit flag raises. Frames that leave the host never default to
  "as fast as possible".
- **Fail closed.** If a check cannot be evaluated (unreadable allowlist,
  `ip` failing), refuse; do not skip.
- **No bypass flags.** Do not add a flag or environment variable that
  disables the interlock. The `BNGLOAD_*` overrides only *relocate* the files,
  for tests.
- **Restore what you change.** A tool that alters NIC or kernel settings
  (as `tstamp check` does with hardware timestamping) restores the previous
  setting on every exit path.
- **Clean up.** Tools that create kernel objects name them with a fixed prefix
  (`bkb*` for kbench) and provide a cleanup command; tests remove everything
  they create, even when they fail.
- **Dry run first.** Scripts that change the host default to printing what
  they would do (`host-prep.sh` needs `--apply`), and never add or remove IP
  addresses.

Every safety rule has a negative test (the tool refuses) as well as a positive
one. A change that weakens a safety check will not be merged.

## Language-specific conventions

### Go

- `gofmt` and `go vet` clean; `go test -race ./...` passes.
- Errors: wrap with context (`fmt.Errorf("open %s: %w", ifname, err)`); no
  `panic` outside `main` and impossible states.
- Raw syscalls belong in `internal/linux`. Other packages use `unsafe` only to
  pass ABI structs to those wrappers or for size checks. Add a test asserting
  the size of every new ABI struct against the kernel's (see `nic_test.go`).
- Namespace work (`setns`) runs on a goroutine locked to its OS thread
  (`runtime.LockOSThread`) for its whole lifetime.
- Commands parse flags with `cli.NewFlagSet`/`cli.Parse`, accept `-o FILE`
  for the JSON report (and `--strict` when they produce a verdict), and print
  a human-readable table plus remedies.
- Tests that need root or real interfaces go in `test/integration` behind the
  `integration` build tag and create their own veth pairs.

### Rust

- `cargo fmt` (see `rustfmt.toml`) and `cargo clippy --all-targets -- -D warnings` clean.
- `unsafe` only in `src/sys.rs`, `src/ring.rs` and `src/tx.rs`, each block with
  a `// SAFETY:` comment explaining why it is sound. Memory shared with the
  kernel is accessed through raw pointers with `read_volatile` /
  `write_volatile`, never through references.
- Kernel ABI structs are defined locally with `#[repr(C)]` and a size test,
  rather than relying on which `libc` release added them.
- Tests needing root are `#[ignore]`d with a reason and live in `tests/`.

### Python

- Must run on Python 3.10: `from __future__ import annotations` in every
  module, `tomllib` with the `tomli` fallback, no 3.11+ only syntax.
- `pyflakes` clean. Type hints on public functions.
- Scenario logic takes its BNG Blaster runner as a parameter so tests can use
  a fake; tests that start BNG Blaster are marked `@pytest.mark.blaster` and
  are deselected by default.
- Lab spec keys are validated in `labspec.parse`, which reports every problem
  with its TOML key path at once; add validation for every new key.

### Shell

- `#!/usr/bin/env bash` with `set -euo pipefail` (or document why not),
  `shellcheck -S warning` clean.
- Never `source /etc/os-release` into the script's own namespace: it defines
  `VERSION`, `ID` and others. Read it in a subshell:
  `OS_VERSION_ID=$(. /etc/os-release && echo "$VERSION_ID")`.
- Commands that may legitimately fail on some drivers are reported and the
  script continues; they are not fatal.

## Documentation and design changes

- **User-visible change** (new flag, new check, new output field): update the
  tool's help text, the README tool table if relevant, and
  [`docs/phase0/runbook.md`](docs/phase0/runbook.md) where the procedure changes.
- **New or changed requirement coverage:** update
  [`docs/phase0/requirements-traceability.md`](docs/phase0/requirements-traceability.md).
- **A decision that changes or refines the design** (a new dependency, a
  different engine, a changed threshold): add an ADR in `docs/adr/` (next
  number, context / decision / consequences) and list it in
  [`docs/adr/README.md`](docs/adr/README.md). Never edit an accepted ADR's
  decision; supersede it.
- **Design document changes** update both
  `docs/design/bngload-technical-design.md` and the PDF in the same pull
  request.
- Cite the design by section or requirement ID (`MR-2`, `SR-19`, `2.1`,
  `TS-04`). Do not invent standard or RFC numbers; link the source for any
  standard you cite.

## Commits and pull requests

Branch from `main` (`fix/tstamp-cross-phc`, `feat/kbench-ipvlan`,
`docs/runbook-ptp`) and open a pull request against `main`.

**Commit messages:** a short imperative subject (≤ 72 characters), prefixed
with the area when it helps — `tstamp:`, `afprobe:`, `lab:`, `scripts:`,
`docs:` — then a body that explains *why*. Reference issues with `Fixes #12`.

```text
afprobe: stop when the RX port is not looped back

If fewer than half of the first step's frames arrive, the TX and RX
ports are not cabled together; continuing would send higher rates into
an unknown device. Stop even with --no-stop and report a cabling finding.
```

**Pull request checklist:**

- [ ] `make test` and `make lint` pass; CI is green.
- [ ] Root-only behaviour covered by `make test-integration`, run locally
      (say so in the description).
- [ ] Hardware-dependent changes: NIC, driver, firmware and kernel you tested
      on, with the JSON report attached.
- [ ] Safety rules followed; new transmit or kernel-changing paths call the
      interlock and have a negative test.
- [ ] Shared rules changed in all three languages, with matching tests.
- [ ] Report schema unchanged, or bumped with `bngload_lab.acceptance` updated.
- [ ] Docs, traceability and ADRs updated as described above.
- [ ] No secrets, real customer data, production addresses or credentials in
      code, configs, fixtures or reports (use documentation ranges such as
      192.0.2.0/24, 2001:db8::/32 and placeholder secrets).

Reviews look first at measurement correctness and safety, then at
consistency across languages, then at style. Keep pull requests focused: one
behaviour change per pull request is easiest to review and to revert.

## Reporting bugs and security issues

**Bugs:** open an issue with the command you ran, the full output, the JSON
report (`-o`), `bngload version` / `afprobe version` / `bngload-lab --version`,
the kernel version, and the NIC model, driver and firmware
(`ethtool -i <if>`). For BNG Blaster runs, include the files from
`runs/…/blaster/<scenario>-…/`.

**Security issues** — a way to bypass the safety interlock, to make a tool
transmit on a non-allowlisted interface, or to run unintended code — should
not be filed as public issues. Contact the maintainer
([@mustafaalam1991](https://github.com/mustafaalam1991)) privately, or use
GitHub's private vulnerability reporting if it is enabled for the repository.

## License

bngload is licensed under the GNU General Public License v3.0 (see
[LICENSE](LICENSE)). By submitting a contribution you agree that it is
licensed under the same terms, and you confirm that you have the right to
submit it — your own work, or code under a GPLv3-compatible licence whose
origin and licence you state in the pull request.
