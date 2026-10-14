# FreeRADIUS for the Phase 0 lab

Phase 0 uses a stock FreeRADIUS 3.2 (Ubuntu package `freeradius`) as the AAA
server. From Phase 3, bngload's own RADIUS emulator replaces it for timing
work (it shares the generator clock and can inject delay and failures).

```sh
sudo apt install freeradius
sudo cp clients.conf /etc/freeradius/3.0/clients.d/bngload.conf    # or append to clients.conf
sudo cp authorize /etc/freeradius/3.0/mods-config/files/authorize
sudo systemctl restart freeradius
# debugging: sudo systemctl stop freeradius && sudo freeradius -X
```

Ubuntu's packaged `clients.conf` may not include a `clients.d` directory;
append the block to `clients.conf` in that case.

- The users file accepts any username with the lab password, which is what
  BNG Blaster's `sub{session-global}@lab` naming needs.
- FreeRADIUS logs every request at default settings; for TS-04 rates set
  `auth = no` in `radiusd.conf` `log {}` so logging does not become the
  bottleneck, and record that in the lab notes.
- Keep the AAA host below half of its own capacity during DUT tests (design
  5.6, AAA separation).
