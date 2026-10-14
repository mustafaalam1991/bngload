# ADR-0002: What Phase 0 builds

- Status: accepted
- Date: 2026-10-14
- Context: design section 15, Phase 0 "Requirements and lab validation"

## Decision

Phase 0 builds the tools that produce the evidence for the Phase 0 acceptance
criteria and deliverables, and nothing of the PPPoE engine itself.

| Design deliverable or criterion | Built in Phase 0 |
| --- | --- |
| Host profile with NIC timestamp support verified | `bngload host profile`, `bngload tstamp check` |
| HW stamps verified on RX and TX (acceptance) | `bngload tstamp check` (SIOCSHWTSTAMP, SO_TIMESTAMPING on AF_PACKET, TX stamps from the error queue) |
| chrony meets MR-2 (acceptance) | `bngload clock check` (adjtimex, step and slew detection, chrony config, PHC) |
| Measured kernel costs for netns/TUN creation | `bngload kbench run` |
| Scale ceilings "to validate in Phase 0" (2.2) | `afprobe` (TPACKET_V3 RX ring, paced `sendmmsg`) |
| Baseline runs with BNG Blaster; TS-01/TS-04-equivalent numbers per DUT (acceptance) | `bngload-lab ts01`, `ts04`, `baseline` |
| Answers to the open questions; every question has an owner (acceptance) | `docs/phase0/open-questions.toml`, `bngload-lab open-questions` |
| Lab topology, DUT configs | `lab/` |
| Signed-off requirements | `docs/phase0/requirements-traceability.md`, sign-off in `docs/phase0/acceptance.md` |
| (all of the above, aggregated) | `bngload-lab acceptance` → `acceptance.md` |
| Host readiness | `bngload health`, `scripts/host-prep.sh` |

Safety interlocks (lab marker, interface allowlist, no IP address on a test
port) are built now, because every Phase 0 tool that transmits needs them.

## Not in Phase 0

PPPoE/PPP/NCP codecs and state machines, the session engine, the scheduler,
the RADIUS emulator, metrics export, the API and the reflector. These start in
Phase 1 per the roadmap. The repository layout leaves room for them under
`internal/` as in design 17.10.

## Consequences

- The TS-01/TS-04 numbers recorded in Phase 0 come from BNG Blaster and are
  *equivalents*: BNG Blaster reports aggregate setup time and rate, not
  per-session phase latencies (see `tools/lab/src/bngload_lab/scenarios.py`).
  They answer open question 8 (build bngload or extend BNG Blaster) and give
  the first `R_max` per DUT; they do not replace bngload's own TS-01/TS-04.
- The acceptance report is reproducible from the JSON files alone, so a run
  directory can be archived and re-evaluated later.
