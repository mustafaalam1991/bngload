# Lab assets

| Path | Contents |
| --- | --- |
| `topology.md` | Reference Phase 0 topology, link requirements and addressing |
| `blaster/lab.example.toml` | Annotated lab spec for one DUT (copy to `blaster/<dut>.toml`) |
| `blaster/selfloop-veth.toml` | Harness self-test over the `blt0`/`blt1` veth pair, no DUT |
| `dut/accel-ppp/` | accel-ppp configuration for an open-source reference DUT |
| `aaa/freeradius/` | FreeRADIUS client and users configuration |

Vendor DUT configurations belong in `dut/<vendor>/` with the firmware
version in the file name; they are a Phase 0 deliverable ("DUT configs").
Strip secrets before committing.
