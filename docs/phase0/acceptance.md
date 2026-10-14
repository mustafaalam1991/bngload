# Phase 0 acceptance checklist and sign-off

The automated part comes from `bngload-lab acceptance` (see the
[runbook](runbook.md), step 11), which writes `acceptance.md` into the run
directory. This page is the human checklist that goes with it.

## Acceptance criteria (design section 15)

| # | Criterion | Automated check | Evidence file | Status |
| --- | --- | --- | --- | --- |
| A1 | Hardware timestamps verified on RX and TX | `data.hardware_rx_tx_verified` in a `bngload tstamp check` report (≥ 99 % of frames carry a hardware stamp in both directions) | `tstamp.json` | ☐ |
| A2 | chrony meets MR-2 | `bngload clock check` verdict PASS: no step, slew ≤ 100 ppm, synchronised, chrony `maxslewrate` ≤ 100 and finite `makestep` | `clock.json` | ☐ |
| A3 | BNG Blaster TS-01/TS-04-equivalent numbers recorded per DUT | for every non-self-loop DUT: a `ts01` report with ≥ 1 established session and a `ts04` report with a verified `R_max` | `ts01-<dut>.json`, `ts04-<dut>.json` | ☐ |
| A4 | Every open question has an owner | `owner` set on every `[[question]]` in `open-questions.toml` | `open-questions.toml` | ☐ |

## Deliverables

IDs match the "Deliverables" table of the generated report.

| # | Deliverable | Where | Status |
| --- | --- | --- | --- |
| D1 | Host profile with NIC timestamp support verified | `hosts/<host>.json` (with `measured`) | ☐ |
| D2 | Host passes `bngload health` (no FAIL) | `health.json` | ☐ |
| D3 | Measured kernel costs for netns/TUN creation | `kbench.json` | ☐ |
| D4 | AF_PACKET I/O ceiling on physical ports | `afprobe.json` | ☐ |
| D5 | BNG Blaster PPPoE 1k-session run per DUT | `baseline-<dut>.json` | ☐ |
| D6.1 | Lab topology documented for this lab | [`lab/topology.md`](../../lab/topology.md) | ☐ |
| D6.2 | DUT configs filed (secrets removed) | `lab/dut/<vendor>/` | ☐ |
| D6.3 | Requirements reviewed and signed off | [requirements-traceability.md](requirements-traceability.md), design requirement tables; then `--signoff` | ☐ |

Beyond A4 (owners), the design also lists *answers* to the open questions as
a deliverable: track `status`, `due` and `answer` in `open-questions.toml`;
answers may land after Phase 0 sign-off if their owners and dates are agreed.

## Phase 0 suggested tests (design section 15)

| Test | How | Status |
| --- | --- | --- |
| Ping/LLDP sanity on the lab segments | by hand (`lldpctl`, ping on the AAA VLAN) | ☐ |
| BNG Blaster PPPoE 1-session run | `bngload-lab ts01 --iterations 1` | ☐ |
| BNG Blaster PPPoE 1k-session run | `bngload-lab baseline` | ☐ |
| Timestamp self-test | `bngload tstamp check` | ☐ |

## Sign-off

| Role | Name | Date | Run directory / commit |
| --- | --- | --- | --- |
| Lab owner | | | |
| Test lead | | | |
| Development lead | | | |

Record the sign-off in the report too: `bngload-lab acceptance … --signoff "Name"`.
