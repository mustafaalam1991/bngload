# bngload

**bngload** is a lab workload generator that emulates PPPoE subscribers
towards a Broadband Network Gateway (BNG) and measures session-establishment
performance. This repository holds **Phase 0 — requirements and lab
validation** from the [technical design](docs/design/bngload-technical-design.md)
([PDF](docs/design/bngload-technical-design.pdf)).

Phase 0 sends no PPPoE of its own. It proves that the generator host, its
NICs, its clock and the lab can produce trustworthy numbers, records the
incumbent tool's (BNG Blaster's) baseline for every DUT, and produces the
evidence for the Phase 0 acceptance criteria:

| Acceptance criterion (design 15) | Tool | Evidence |
| --- | --- | --- |
| HW timestamps verified on RX and TX | `bngload tstamp check` | `tstamp.json` |
| chrony meets MR-2 | `bngload clock check` | `clock.json` |
| BNG Blaster TS-01/TS-04-equivalent numbers per DUT | `bngload-lab ts01`, `ts04` | `ts01-<dut>.json`, `ts04-<dut>.json` |
| Every open question has an owner | `bngload-lab open-questions` | [`docs/phase0/open-questions.toml`](docs/phase0/open-questions.toml) |

`bngload-lab acceptance` turns a run directory into `acceptance.md`.

> **Authorised lab use only.** Every tool that transmits refuses to start
> unless the host carries the lab marker `/etc/bngload/lab-host`, the
> interface is listed in `/etc/bngload/interfaces.allow`, and the interface
> has no IP address (design SR-11, SR-19). Do not point it at production.

## Components and languages

Each module uses the language that fits its requirements
([ADR-0001](docs/adr/0001-language-per-module.md)):

| Component | Language | What it does |
| --- | --- | --- |
| [`cmd/bngload`](cmd/bngload), [`internal/`](internal) | Go (stdlib only, [ADR-0003](docs/adr/0003-stdlib-only-go.md)) | `health`, `host profile`, `clock check`, `tstamp check`, `kbench run` — the seed of the v1 Go product |
| [`engines/afprobe`](engines/afprobe) | Rust (`libc` only) | AF_PACKET I/O ceiling: TPACKET_V3 RX ring + paced `sendmmsg`, GC-free reference for the design 2.1 exit criterion |
| [`tools/lab`](tools/lab) | Python (stdlib at run time) | `bngload-lab`: BNG Blaster TS-01/TS-04 equivalents, open-question register, acceptance report |
| [`scripts/`](scripts) | Bash | dependency install, host preparation, veth dry-run pair, full Phase 0 run |

All tools write the same JSON envelope (`schema: bngload.report/v1`) with a
verdict (`INFO`/`PASS`/`WARN`/`FAIL`), a one-line summary, findings with
design references, and the raw data.

## Quick start

On a bare-metal Ubuntu 22.04 or 24.04 host with two test ports cabled
back-to-back (see [`lab/topology.md`](lab/topology.md)):

```sh
git clone https://github.com/mustafaalam1991/bngload.git && cd bngload
sudo scripts/install-deps-ubuntu.sh        # apt packages, Go, Rust
sudo scripts/install-bngblaster.sh         # BNG Blaster release .deb
make && sudo make install                  # bngload, afprobe, bngload-lab → /usr/local/bin

# Prepare the test ports (dry run first, then --apply) and mark the lab host
# ens2f0 = access port to the DUT, ens2f1 <-> ens3f0 = back-to-back calibration pair
sudo scripts/host-prep.sh -i ens2f0 -i ens2f1 -i ens3f0 --qinq
sudo scripts/host-prep.sh -i ens2f0 -i ens2f1 -i ens3f0 --qinq --apply \
     --mark-lab-host --lab bng-lab-1 --owner "Your Team"

sudo bngload health --interface ens2f0 --interface ens2f1 --interface ens3f0
```

Then follow the [Phase 0 runbook](docs/phase0/runbook.md). The whole
measurement set runs with one command:

```sh
cp lab/blaster/lab.example.toml lab/blaster/accel-ppp-01.toml   # edit for your DUT
sudo scripts/phase0-run.sh --tx-if ens2f1 --rx-if ens3f0 --access-if ens2f0 \
     --spec lab/blaster/accel-ppp-01.toml --host-name gen-01
# → runs/<timestamp>/acceptance.md and hosts/gen-01.json
```

### Dry run without NICs or a DUT

```sh
sudo scripts/lab-veth.sh up && eval "$(scripts/lab-veth.sh env)"
sudo -E scripts/phase0-run.sh --tx-if blt0 --rx-if blt1 \
     --spec lab/blaster/selfloop-veth.toml --quick
```

The veth pair has no hardware clock and BNG Blaster answers itself
(self-loop), so the acceptance report correctly shows the criteria as not
met; every tool and the report pipeline are exercised end to end.

## The tools

| Command | Purpose | Design refs |
| --- | --- | --- |
| `bngload health --interface IF …` | Read-only host readiness: kernel, privileges, lab marker and allowlist, CPU isolation, sysctls, clock, tools, and per port: link, no IP, MTU, VLAN offload, coalescing, rings, timestamping | 9, MR-1/2/3/6, SR-11/19 |
| `bngload host profile --interface IF …` | Records the host (CPU, memory, kernel, NIC driver/firmware, offloads, timestamping caps, clock) | FR-161 |
| `bngload clock check` | Watches the system clock for steps and slew, checks chrony config and sync state; optional PHC read | MR-2, MR-3 |
| `bngload tstamp check --tx-if A --rx-if B` | Self-test frames (EtherType 0x88B5, ≤ 1000/s) between two cabled ports; verifies software and hardware RX/TX stamps; reports kernel one-way latency and the user-space error stamps remove | MR-1, MR-3 |
| `bngload kbench run` | Creation/teardown cost and kernel memory of netns, netns+TUN (tier K), veth and macvlan contexts | 2.3, 15 |
| `afprobe run --tx-if A --rx-if B` | Steps a paced frame rate until loss; reports the I/O ceiling, per-core TX rate, schedule lag, ring drops | 2.1, 2.2, MR-6 |
| `bngload-lab ts01 -f SPEC` | TS-01 equivalent: repeated 1-session BNG Blaster runs → setup-time distribution with CIs | TS-01 |
| `bngload-lab ts04 -f SPEC` | TS-04 equivalent: binary search of the setup rate, then verification → `R_max` | TS-04 |
| `bngload-lab baseline -f SPEC` | One N-session run (default 1000) | 15 suggested tests |
| `bngload-lab open-questions FILE` | Checks the open-question register (owners, status, due dates) | 17.7 |
| `bngload-lab acceptance DIR…` | Phase 0 acceptance report (Markdown + JSON) and host-profile merge | 15 |

Exit codes are the same everywhere: `0` ok, `1` FAIL (or WARN with
`--strict`), `2` usage error, `3` runtime error.

## Repository layout

```text
cmd/bngload/            Go CLI entry point and commands
internal/               Go packages: cli, report, stats, safety, linux, nic, netlinkx,
                        pktio/afpacket, clock, tstamp, kbench, sysinfo, health, hostprofile, version
engines/afprobe/        Rust AF_PACKET I/O-ceiling probe
tools/lab/              Python bngload-lab harness (package bngload_lab, pytest tests)
test/integration/       Go integration tests over veth (build tag "integration", root)
lab/                    topology, BNG Blaster lab specs, accel-ppp and FreeRADIUS configs
scripts/                install, host-prep, lab-veth, phase0-run
hosts/                  host profiles with measured ceilings (one JSON per generator host)
docs/design/            technical design (Markdown + PDF)
docs/phase0/            runbook, acceptance checklist, traceability, open questions
docs/adr/               architecture decision records
```

## Development

```sh
make test                      # Go vet+test, cargo test, pytest — no root needed
sudo make test-integration     # veth-based tests (needs scripts/lab-veth.sh up for BNG Blaster)
make lint                      # go vet, clippy -D warnings, shellcheck, pyflakes
```

CI (`.github/workflows/ci.yml`) runs the unit tests on every push and the
root integration tests plus a `--quick` Phase 0 smoke run (veth, BNG Blaster
self-loop) on GitHub's Ubuntu runners.

See [CONTRIBUTING.md](CONTRIBUTING.md) for setup, the rules every change
follows (report format, safety interlock, measurement correctness,
dependencies) and the pull-request checklist. Report security issues privately as
described in [SECURITY.md](SECURITY.md).

## Status

Phase 0 tooling complete; Phase 0 *results* depend on the lab (NICs with
hardware timestamping, chrony, DUT access, owners for the open questions).
Phase 1 (PPPoE discovery prototype) starts once the acceptance report passes.

## License

GNU General Public License v3.0 — see [LICENSE](LICENSE).
