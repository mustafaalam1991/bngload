# ADR-0003: Phase 0 Go code uses the standard library only

- Status: accepted (revisit at the start of Phase 1)
- Date: 2026-10-14
- Context: design 17.1 names `golang.org/x/sys/unix`, `vishvananda/netlink`,
  `cilium/ebpf` and others for the full product; SR-24 (supply chain)

## Decision

The Phase 0 Go module has no third-party dependencies. The few kernel
interfaces it needs are implemented directly:

| Need | Implementation |
| --- | --- |
| ioctls (`SIOCETHTOOL`, `SIOCSHWTSTAMP`) | `internal/linux` (struct `ifreq` with a data pointer), `internal/nic` |
| rtnetlink (veth, macvlan, addresses, routes, link dump) | `internal/netlinkx`, about 340 lines over raw `AF_NETLINK` sockets from the `syscall` package |
| `setns`, `ppoll`, `clock_gettime` on arbitrary clock ids | raw syscalls in `internal/linux` with per-architecture numbers (amd64, arm64) |
| AF_PACKET with `SO_TIMESTAMPING` and the error queue | `internal/pktio/afpacket` |

## Why

- Lab hosts are often air-gapped or behind strict proxies; `go build` must
  work without a module proxy.
- Phase 0 needs only a handful of calls; the code is small, tested, and keeps
  `unsafe` to the kernel-interface packages (raw syscalls only in
  `internal/linux`; elsewhere only to pass ABI structs), which is easy to
  audit (SR-25).
- An empty `go.sum` is the simplest possible supply-chain story for the first
  binaries that run as root on a lab host (SR-24).

## Consequences

- Phase 1 brings TPACKET_V3 rings, BPF fanout and the RADIUS codec. At that
  point adopting `golang.org/x/sys/unix` (and possibly `cilium/ebpf`) is
  expected; vendor them (`go mod vendor`) so offline builds keep working, and
  record the decision in a new ADR.
- `internal/linux` and `internal/netlinkx` are deliberately narrow; replace
  them rather than grow them when the dependencies arrive.
