# afprobe — AF_PACKET I/O ceiling probe

`afprobe` measures how many frames per second the generator host can move
through the AF_PACKET path that the bngload engine will use (design 2.2:
TPACKET_V3 RX ring, batched `sendmmsg` TX), and at what CPU cost. It is the
Phase 0 validation of the 2.2 scale ceilings and the GC-free reference for the
design 2.1 exit criterion (ADR-0001).

```text
  --tx-if                                   --rx-if
 ┌──────────────────┐   back-to-back    ┌──────────────────────┐
 │ TX thread        │ ─────cable──────▶ │ RX thread            │
 │ paced sendmmsg   │   0x88B5 frames   │ TPACKET_V3 ring walk │
 │ step, seq, nonce │                   │ loss / dup / reorder │
 └──────────────────┘                   └──────────────────────┘
```

Each rate step sends `rate × duration` sequence-numbered frames, waits for a
drain period and compares what arrived. A step is **clean** when TX reached
≥ 99 % of the target, loss and `ENOBUFS` refusals stay within `--max-loss`
(default 1e-4) and no send error occurred. The highest clean step is the
**I/O ceiling**.

## Usage

```sh
sudo afprobe run --tx-if ens2f1 --rx-if ens3f0 --tx-cpu 2 --rx-cpu 3 -o afprobe.json
sudo afprobe run --tx-if ens2f1 --rx-if ens3f0 --rates 1M,2M,3M,max --duration 10s
sudo afprobe run --tx-if ens2f1 --rx-if ens3f0 --dut-max-cps 2500   # design 2.1 headroom check
afprobe help
```

| Output | Meaning |
| --- | --- |
| `tx pps` | frames accepted by the kernel per second of the step |
| `rx pps` | from kernel RX timestamps of the first and last frame |
| `loss` | (sent − received unique) / sent |
| `lag p99` | TX schedule lag (actual − due time per batch), paced steps only |
| `tx pps/core` | frames per second of time spent *inside* `sendmmsg` (pacing waits excluded) — the per-core TX capacity |
| `irq cores` | host CPU time in hard/soft IRQ during the step, in cores — where the kernel's RX work runs |
| `ring` | frames dropped because the TPACKET_V3 ring was full |

Findings: ports physical (WARN on veth), ceiling (PASS/FAIL against
`--target-pps`, INFO otherwise), design 2.1 headroom (`--dut-max-cps`),
schedule lag p99 < 1 ms at the highest clean paced rate, no ring drops, thread
pinning, cabling.

## Safety

Same interlock as every bngload tool: lab marker, both interfaces in the
allowlist, no IP address on either. In addition:

- With `--rx-if`, frames go to the RX port's MAC; if the first step loses
  more than half its frames the ports are not cabled together and the run
  stops (even with `--no-stop`).
- Without `--rx-if` (TX-only, frames leave the host) the rate is capped at
  20 kpps (design SR-14 default) unless `--tx-only-cap` raises it, `max` is
  refused and `--dst-mac` is mandatory.

## Build and test

```sh
cargo build --release --locked                           # target/release/afprobe
cargo test --locked                                      # unit tests
sudo -E cargo test --release --test veth -- --ignored    # creates its own veth pairs
```

Dependencies: `libc` only. All `unsafe` code is in `src/sys.rs`,
`src/ring.rs` and `src/tx.rs`; `cargo doc --open` has the module map.

## Reading the numbers

- On veth (dry runs) the kernel does the RX work inside the sender's
  `sendmmsg`, so TX per-core figures include it and the RX thread looks
  nearly free. Only physical ports give NIC numbers; the report flags
  virtual ports.
- The ceiling is an *upper bound* for any engine on this host: no protocol
  work is done. The generator ceiling for DUT tests comes from TS-16 against
  the reflector (Phase 6), and DUT tests run at ≤ ⅓ of it (design 5.6).
- `setups_per_s_equivalent` divides the ceiling by `--frames-per-setup`
  (default 13: the client-side frames of a dual-stack PAP session as counted
  by BNG Blaster's protocol statistics — PADI+PADR 2, LCP 3, PAP 1, IPCP 4,
  IPv6CP 2, RS 1). Adjust it to your profile (CHAP, DHCPv6-PD, retries).
