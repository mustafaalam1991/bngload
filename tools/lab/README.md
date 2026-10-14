# bngload-lab — Phase 0 lab harness

Python package `bngload_lab`, command `bngload-lab`. It drives BNG Blaster to
record the Phase 0 baseline numbers for every DUT, checks the open-question
register and builds the Phase 0 acceptance report from the JSON reports of
all Phase 0 tools.

```sh
bngload-lab check-spec     -f lab/blaster/<dut>.toml
bngload-lab render-config  -f lab/blaster/<dut>.toml --sessions 10 --rate 5
sudo bngload-lab ts01      -f lab/blaster/<dut>.toml [--iterations N]
sudo bngload-lab ts04      -f lab/blaster/<dut>.toml [--quick]
sudo bngload-lab baseline  -f lab/blaster/<dut>.toml [--sessions N --rate R]
bngload-lab open-questions docs/phase0/open-questions.toml [--markdown]
bngload-lab acceptance runs/<dir> --open-questions docs/phase0/open-questions.toml \
            [--host-profile-out hosts/<host>.json] [--signoff NAME]
```

Reports go to `--out-dir` (default `runs/`) as `<scenario>-<dut>.json`; every
BNG Blaster config, log and raw report is kept under
`<out-dir>/blaster/<scenario>-<dut>-<timestamp>/`.

## Scenarios

| Scenario | What runs | Pass rule (DUT verdict) |
| --- | --- | --- |
| `ts01` | `iterations` × (1 session, hold, graceful teardown), one BNG Blaster process each | 100 % established, 0 retransmissions, setup p99 ≤ `T_est`/10 |
| `ts04` | binary search on the offered rate between `min_rate` and `max_rate`; each step offers `rate × step_s` sessions; then `ceil(verify_s/step_s)` verification runs at the found rate (exponential back-off if they fail) | step passes on success ratio ≥ 0.999, retransmission ratio ≤ 1 %, last session ≤ `T_est` behind schedule |
| `baseline` | one run of `sessions` at `rate` | all sessions established |

Why "equivalent": BNG Blaster reports one aggregate `setup-time-ms` and
`setup-rate-cps` per run and only the *current* number of established
sessions. One session per run gives TS-01 a per-session distribution; TS-04
keeps sessions up for the whole step so the counters are cumulative, and
judges latency by the tail lag. Details are in `scenarios.py`.

Self-loop specs (`dut.mode = "selfloop"`, BNG Blaster's A10NSP interface
playing the BNG) validate the harness and never count as DUT results.

## Development

```sh
pip install -e '.[test]'          # or: PYTHONPATH=src python3 -m bngload_lab …
python3 -m pytest                 # unit tests (fake BNG Blaster)
sudo -E python3 -m pytest -m blaster   # real BNG Blaster over blt0/blt1 (scripts/lab-veth.sh up)
```

Runs on Python ≥ 3.10 with the standard library (plus `tomli` on 3.10).
`sudo make install` installs it as plain files with a wrapper script — no pip,
no network.
