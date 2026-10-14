# accel-ppp reference DUT

[accel-ppp](https://accel-ppp.org/) is an open-source BNG. It is not a
production DUT but a reproducible reference: every Phase 0 tool and lab
procedure can be rehearsed against it before vendor DUT time is booked.

## Host setup (separate machine or VM with a passthrough NIC)

1. Build and install accel-ppp from source with the `vlan_mon` kernel module
   (`cmake -DBUILD_VLAN_MON_DRIVER=TRUE …`), then `modprobe vlan_mon`.
2. Create the S-VLAN interfaces on the access port (QinQ outer tag 802.1ad
   or 802.1Q — match the generator's spec):

   ```sh
   for s in $(seq 100 199); do
     ip link add link ens2f0 name ens2f0.$s type vlan proto 802.1Q id $s
     ip link set ens2f0.$s up
   done
   ip link set ens2f0 mtu 1508 up
   ```

3. Copy `accel-ppp.conf` to `/etc/accel-ppp.conf`, adjust interface names,
   addresses and the RADIUS secret, and start `accel-pppd -d -c /etc/accel-ppp.conf`.
4. Point RADIUS at the lab AAA (`lab/aaa/freeradius`).
5. Check with `accel-cmd show sessions` while running
   `bngload-lab ts01 -f lab/blaster/accel-ppp-01.toml --iterations 3`.

## Things that change results — record them in the DUT spec

- `max-starting`, `padi-limit` and `[radius] req-limit` cap the setup rate
  (TS-04) independently of CPU.
- `thread-count` and the host's CPU model.
- `vlan-mon` creates interfaces on demand; the first session on a new C-VLAN
  is slower than later ones. Keep this in mind when reading TS-01 numbers.
