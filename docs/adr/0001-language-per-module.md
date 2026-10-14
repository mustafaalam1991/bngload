# ADR-0001: Implementation language per module

- Status: accepted (Phase 0)
- Date: 2026-10-14
- Context: design section 2.1 (Go is the v1 language, with an exit criterion
  for a second engine), Phase 0 deliverables (section 15)

## Decision

Each Phase 0 module uses the language whose strengths match that module's
functional and non-functional requirements. Modules talk to each other only
through files in one shared format (the `bngload.report/v1` JSON envelope),
so each one can be replaced without touching the others.

| Module | Language | Requirements that decided it |
| --- | --- | --- |
| `bngload` CLI: `health`, `host profile`, `clock check`, `tstamp check`, `kbench run` | **Go** | It is the seed of the v1 product, which the design fixes as Go (2.1). Needs netlink, ioctl, `SO_TIMESTAMPING`, `setns` on locked OS threads, a single static binary for bare metal, and a fast build/test loop. Timing comes from kernel or NIC stamps, so Go's GC affects capacity, not accuracy (2.1). |
| `afprobe` I/O-ceiling probe | **Rust** | Must measure the AF_PACKET path *without* a garbage collector or runtime scheduler in the way, so its ceiling is a clean reference for the Go engine (2.1 exit criterion, TS-16) and for the "validate in Phase 0" scale ceilings of 2.2. Needs `mmap` rings, `sendmmsg`, CPU pinning and deterministic pacing. It is also the first concrete step towards the Rust engine the design names as the Phase 7 contingency. |
| `bngload-lab` harness | **Python** | Orchestrates BNG Blaster processes, parses JSON/TOML, does statistics on hundreds of numbers and writes Markdown. Nothing is timing-critical (the numbers come from BNG Blaster and the other tools). The design already assigns Python to the test harness (2.1, "Other options considered"). |
| Host and lab scripts | **Bash** | Thin wrappers around `apt`, `ethtool`, `sysctl` and `ip`, run once per host. |

## Consequences

- Three toolchains to install (`scripts/install-deps-ubuntu.sh` does it) and
  three test runners (`make test` runs all of them).
- Shared rules are implemented three times: the safety interlock (lab marker,
  interface allowlist, no IP address) and the percentile definition (type 7).
  Each implementation has tests with the same reference values, and the
  report envelope is identical in all three.
- Every tool keeps its dependency set minimal (ADR-0003 for Go; Rust uses
  only `libc`; Python only the standard library at run time), so the whole
  Phase 0 toolkit builds and runs on an air-gapped lab host.
- If Phase 1+ measurements show the Go engine is within the 2.1 exit criterion,
  `afprobe` stays a calibration tool. If not, its ring and TX code are the
  starting point for the Rust engine behind `PacketIO`.
