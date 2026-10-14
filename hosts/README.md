# Host profiles

One JSON file per generator host (`hosts/<host-name>.json`), written by
`scripts/phase0-run.sh` (via `bngload-lab acceptance --host-profile-out`).
It is the `bngload host profile` report with a `data.measured` section added:

- `afprobe_ceiling` — AF_PACKET I/O ceiling and per-core TX rate;
- `kbench` — netns, TUN, veth and macvlan creation costs;
- `timestamping` — whether hardware RX/TX stamps were verified.

Commit the profile of every lab host: later phases read the calibrated
ceiling from it (design 5.6, operate below one third of the ceiling) and the
run manifest records which profile a result was produced on (FR-161).
