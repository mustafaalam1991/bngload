# Requirements traceability — Phase 0

Which design requirements Phase 0 code touches, where, and how it is tested.
"Partial" means Phase 0 implements the part that applies without a PPPoE
engine; the rest is scheduled in the phase named.

| Requirement (design) | Phase 0 implementation | Tests | Coverage |
| --- | --- | --- | --- |
| **MR-1** intervals only within one clock domain | `internal/tstamp`: software and hardware stamps kept apart; hardware one-way latency only when TX and RX share a PHC, otherwise only cross-PHC jitter is reported | `tstamp_test.go` (`TestAnalyseHardwareCrossPHC`) | Full for Phase 0 tools |
| **MR-2** no clock steps; chrony slew-only | `internal/clock` (adjtimex polling, REALTIME−MONOTONIC step detection, slew vs MONOTONIC_RAW, chrony config parse), `bngload clock check`, health `clock.*`; `host-prep.sh` writes `maxslewrate 100`, `makestep 1 3` | `clock_test.go` (synthetic steps and slew, chrony parsing) | Full (the per-run interlock arrives with the run controller, Phase 6) |
| **MR-3** hardware stamps on TX and RX | `internal/nic` (`ETHTOOL_GET_TS_INFO`, `SIOCSHWTSTAMP` with restore), `internal/pktio/afpacket` (`SO_TIMESTAMPING`, error-queue TX stamps), `bngload tstamp check` | `nic_test.go`, `tstamp_test.go`, integration `TestTimestampSelfTestOnVeth` (software path; hardware path needs a PHC NIC) | Full; hardware path verified only on lab NICs |
| **MR-4** 64-bit ns stamps | all stamps are `int64` ns (Go) / `u64` ns (Rust) | — | Full |
| **MR-6** RX coalescing off; TPACKET_V3 retire ≤ 1 ms | health `nic.<if>.coalescing`; `host-prep.sh` sets `rx-usecs 0`; `afprobe` ring `retire_ms` default 1 | `ring.rs` config tests | Full |
| **FR-161** reproducibility manifest | `bngload host profile` (kernel, CPU, NIC driver/firmware, offloads, timestamping, clock); every report carries tool version, host and kernel | `sysinfo_test.go` | Partial (seed/config hash come with the run controller, Phase 6) |
| **SR-11** interface allowlist | `internal/safety`, `afprobe::safety`, `bngload_lab.safety`: refuse any interface not in `/etc/bngload/interfaces.allow` | `safety_test.go`, `safety.rs` tests, `test_acceptance.py::test_safety_rules` | Full |
| **SR-19** production-environment checks | lab marker required; no IPv4/global IPv6 on a test port (same three implementations) | as SR-11 | Partial (passive listen, foreign PPPoE/DHCP/RA, MAC count and LLDP checks need the engine, Phase 1–6) |
| **SR-14** TX rate limits | `tstamp` ≤ 1000 frames/s; `afprobe` TX-only runs capped at 20 kpps unless raised, `max` refused; back-to-back runs stop if the ports are not looped | `cli.rs` tests, `findings` tests | Partial (token buckets belong to the engine) |
| **SR-13** dry run | `host-prep.sh` is dry-run by default; `bngload-lab render-config` shows the exact BNG Blaster config | — | Partial |
| **SR-24** supply chain | stdlib-only Go (ADR-0003), Rust `libc` only with a committed `Cargo.lock`, Python stdlib at run time; Go tarball checksum verified by the installer | CI | Partial (SBOM, signing: Phase 10) |
| **SR-25** input hardening | Go `unsafe` only in the kernel-interface packages (`internal/linux` for raw syscalls; `nic`, `pktio/afpacket`, `kbench`, `netlinkx` to pass ABI structs or check sizes); Rust `unsafe` only in `afprobe` `sys`/`ring`/`tx`; ring walk bounds-checked; frame parsers length-checked | `frame.rs`, `tstamp_test.go` (`TestParseFrame`) | Partial (fuzzing starts with the codecs, Phase 1) |
| **2.1 exit criterion** (3 × DUT max setup rate) | `afprobe --dut-max-cps` checks the I/O-path share; schedule lag p99 < 1 ms at the highest clean paced rate | `probe.rs` findings tests | Partial (Go worker part is TS-16, Phase 6) |
| **2.2 scale ceilings, "validate in Phase 0"** | `afprobe` (TPACKET_V3 RX ring, `sendmmsg`, per-core rate, ring drops) | `afprobe` unit + veth tests | Full |
| **2.3 kernel-context tiers** | `bngload kbench` (netns, netns+TUN, veth, macvlan cost and cleanup lag) | integration `TestKbenchScenarios` | Full |
| **TS-01** baseline single session | `bngload-lab ts01` (BNG Blaster equivalent) | `test_scenarios.py`, `-m blaster` self-loop | Equivalent (no phase latencies) |
| **TS-04** maximum setup rate | `bngload-lab ts04`: binary search + verification | `test_scenarios.py` (fake DUT), self-loop runs | Equivalent (aggregate counters) |
| **17.7** open questions | `docs/phase0/open-questions.toml`, `bngload-lab open-questions` | `test_acceptance.py` | Full |
| **15 Phase 0 acceptance** | `bngload-lab acceptance` | `test_acceptance.py` | Full |

## Statistics conventions shared by all tools

- Percentiles: linear interpolation between closest ranks (Hyndman & Fan
  type 7), tested against the same reference values in Go
  (`stats_test.go`), Rust (`stats.rs`) and Python (`test_stats.py`).
- Confidence intervals: percentile bootstrap with a fixed seed (TS-01) and
  Wilson score intervals for success ratios.
