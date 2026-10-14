# Phase 0 runbook

Run these steps on the generator host. Every command that transmits checks
the safety interlock first; nothing here is meant for a host or port that
touches production.

Port names below follow [`lab/topology.md`](../../lab/topology.md):
`ens2f0` is the DUT-facing access port, `ens2f1` and `ens3f0` are the
calibration pair cabled back-to-back.

## 1. Install

```sh
sudo scripts/install-deps-ubuntu.sh       # apt packages, Go (tarball, checksum-verified), Rust (rustup)
sudo scripts/install-bngblaster.sh        # BNG Blaster .deb; --source to build from git
make                                      # bin/bngload, engines/afprobe/target/release/afprobe
make test                                 # unit tests, no root needed
sudo make install                         # /usr/local/bin/{bngload,afprobe,bngload-lab}
```

Offline hosts: build on a connected machine with the same Ubuntu release
(`make`, then copy `bin/bngload`, the `afprobe` binary and `tools/lab/src`),
or vendor the Rust crate (`cargo vendor`). The Go and Python code have no
third-party run-time dependencies.

## 2. Prepare the host

```sh
sudo scripts/host-prep.sh -i ens2f0 -i ens2f1 -i ens3f0 --qinq            # dry run: read the plan
sudo scripts/host-prep.sh -i ens2f0 -i ens2f1 -i ens3f0 --qinq --apply --persist
```

`host-prep.sh` never adds or removes IP addresses; if a port has one it
stops and tells you. Remove the address yourself (and from netplan) only
after confirming the port is not in use.

Things the script deliberately leaves to you, because they need a reboot or
a judgement call — record what you chose in the lab notes:

- CPU isolation for the generator and probe threads: kernel command line
  `isolcpus=… nohz_full=… rcu_nocbs=…` on cores of the test NIC's NUMA node.
- Keep irqbalance from moving test-NIC interrupts (`IRQBALANCE_BANNED_CPUS`
  or pin the NIC IRQs to housekeeping cores).
- BIOS: disable deep C-states, fix the CPU frequency or turbo setting.

## 3. Mark the lab host (once, by the lab owner)

```sh
sudo scripts/host-prep.sh -i ens2f0 -i ens2f1 -i ens3f0 --apply \
     --mark-lab-host --lab bng-lab-1 --owner "Telco Cloud Lab"
```

This writes `/etc/bngload/lab-host` (with the lab name and owner, copied into
reports) and `/etc/bngload/interfaces.allow`. Without both, no Phase 0 tool
transmits.

## 4. Health check

```sh
sudo bngload health --interface ens2f0 --interface ens2f1 --interface ens3f0
```

Fix every `FAIL`; read every `WARN` — each line names the design reference
and a remedy. Add `--no-qinq` if the access port is untagged or single-tagged
(the MTU ≥ 1508 check is then skipped).

## 5. Clock (acceptance A2, MR-2)

```sh
sudo bngload clock check --duration 300s --phc-interface ens2f1 -o runs/manual/clock.json
```

PASS needs: no step in the window, slew within 100 ppm, kernel clock
synchronised, chrony configured with `maxslewrate` ≤ 100 and `makestep` with
a finite limit. With a PTP grandmaster in the lab, also run `ptp4l` and
`phc2sys` and record it in OQ-05.

## 6. Timestamps (acceptance A1, MR-1, MR-3)

```sh
sudo bngload tstamp check --tx-if ens2f1 --rx-if ens3f0 --count 1000 -o runs/manual/tstamp.json
```

Look for `hardware_rx_tx_verified: true`. If `--mode auto` falls back to
software, the NIC or driver does not advertise hardware stamping for all RX
frames (`ts_filter_all` in `health`); try `--mode hw` to see the exact error,
check the driver/firmware, and record the outcome in OQ-05.

If the access port's NIC differs from the calibration pair, repeat the test
on a pair of ports of the access NIC model before cabling it to the DUT.

## 7. Kernel costs (deliverable D3)

```sh
sudo bngload kbench run --scenario all --count 1000 --parent ens2f1 -o runs/manual/kbench.json
```

Creates and deletes 1000 network namespaces, namespace+TUN contexts, veth
pairs and macvlans (named `bkb*`) and reports creation time, kernel memory per
object and teardown lag. If interrupted, `sudo bngload kbench cleanup`
removes leftovers.

## 8. I/O ceiling (deliverable D4)

```sh
sudo afprobe run --tx-if ens2f1 --rx-if ens3f0 --tx-cpu 2 --rx-cpu 3 -o runs/manual/afprobe.json
```

Pin the threads to isolated cores on the NIC's NUMA node. The default rate
steps go to 1.6 Mpps and then unpaced; add steps if every step is clean (the
report then says "lower bound"). With the DUT's `R_max` from step 9, rerun
with `--dut-max-cps R_MAX` to check the design 2.1 headroom (3 × `R_max`
setups/s worth of frames).

## 9. BNG Blaster baselines per DUT (acceptance A3)

1. Bring up the AAA server (`lab/aaa/freeradius`) and the DUT
   (`lab/dut/<vendor>/`). Confirm one session by hand if needed.
2. Copy `lab/blaster/lab.example.toml` to `lab/blaster/<dut>.toml` and set
   `dut.name`, firmware, access interface, VLAN ranges, PPP auth and the
   TS-04 bounds (`max_rate` ≤ ⅓ of BNG Blaster's own ceiling on this host;
   `max_sessions` ≤ the DUT's licence).
3. Validate and preview:

   ```sh
   bngload-lab check-spec -f lab/blaster/<dut>.toml
   bngload-lab render-config -f lab/blaster/<dut>.toml --sessions 10 --rate 5
   ```

4. Run:

   ```sh
   sudo bngload-lab ts01 -f lab/blaster/<dut>.toml --out-dir runs/manual
   sudo bngload-lab baseline -f lab/blaster/<dut>.toml --out-dir runs/manual
   sudo bngload-lab ts04 -f lab/blaster/<dut>.toml --out-dir runs/manual
   ```

   TS-04 with the defaults takes 30–60 minutes per DUT (30 s steps plus a
   600 s verification). Every BNG Blaster config, log and report is kept under
   `runs/manual/blaster/` for the record. Clear the DUT's sessions between
   scenarios.

## 10. Open questions (acceptance A4)

Edit [`open-questions.toml`](open-questions.toml): give every question an
`owner` (and ideally a `due` date); fill `answer` and `evidence` as answers
arrive. Check with:

```sh
bngload-lab open-questions docs/phase0/open-questions.toml --markdown
```

## 11. Acceptance report

```sh
bngload-lab acceptance runs/manual --open-questions docs/phase0/open-questions.toml \
    --out-dir runs/manual --host-profile-out hosts/$(hostname -s).json --signoff "Name"
```

`runs/manual/acceptance.md` lists A1–A4 with evidence and the deliverables
D1–D6. Commit the host profile and attach the run directory to the Phase 0
sign-off ([acceptance.md](acceptance.md)).

## All of the above in one command

```sh
sudo scripts/phase0-run.sh --tx-if ens2f1 --rx-if ens3f0 --access-if ens2f0 \
     --spec lab/blaster/<dut>.toml --host-name gen-01
```

## Troubleshooting

| Symptom | Likely cause |
| --- | --- |
| `refused by safety interlock` | missing `/etc/bngload/lab-host`, interface not in `interfaces.allow`, or an IP address on the port |
| `tstamp`: mode `sw` on a NIC that should do hardware | driver lacks `HWTSTAMP_FILTER_ALL` (PTP-only filters), or another process (ptp4l) owns the config |
| `tstamp`: first frames without RX stamp | expected and handled: the kernel enables timestamping lazily; the tool sends uncounted warm-up frames |
| `afprobe`: `cabling_suspect` | the TX and RX ports are not connected to each other |
| `afprobe`: `ENOBUFS` refusals | TX queue full with qdisc bypass: the NIC is the limit at that rate |
| BNG Blaster `control socket … did not appear` | BNG Blaster failed to start — see the `.log` next to the config in `runs/…/blaster/` |
| TS-04 fails even at `min_rate` | DUT/AAA not ready, auth mismatch, or VLAN ranges wrong — run `ts01 --iterations 1` and read the BNG Blaster log |
