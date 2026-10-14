#!/usr/bin/env bash
# lab-veth.sh — create or remove the blt0/blt1 veth pair used to dry-run the
# Phase 0 tools without a NIC or DUT (tstamp, afprobe and the BNG Blaster
# self-loop all accept it; reports flag the ports as virtual).
#
#   sudo scripts/lab-veth.sh up        # create blt0<->blt1, MTU 1508, no IPv6
#   sudo scripts/lab-veth.sh down      # remove the pair
#   sudo scripts/lab-veth.sh status
#
# For the safety interlock, point the tools at a scratch marker and allowlist
# instead of touching /etc/bngload:
#
#   eval "$(scripts/lab-veth.sh env)"
set -euo pipefail

A=${BLT_A:-blt0}
B=${BLT_B:-blt1}
SCRATCH=${BLT_SCRATCH:-/tmp/bngload-veth}

case "${1:-}" in
  up)
    [[ $EUID -eq 0 ]] || { echo "run as root" >&2; exit 2; }
    if ip link show "$A" >/dev/null 2>&1; then
      echo "$A already exists"
    else
      ip link add "$A" type veth peer name "$B"
    fi
    for i in "$A" "$B"; do
      # Absent when the kernel runs without IPv6: nothing to disable then.
      [[ -e /proc/sys/net/ipv6/conf/$i ]] && sysctl -q -w "net.ipv6.conf.$i.disable_ipv6=1"
      ip link set dev "$i" mtu 1508 up
    done
    # The scratch directory holds the interlock files and is written by root,
    # so it must be root-owned and not writable by anyone else: a directory
    # (or symlink) pre-created by another user at this path is refused.
    if [[ -e "$SCRATCH" || -L "$SCRATCH" ]]; then
      if [[ -L "$SCRATCH" || ! -d "$SCRATCH" || "$(stat -c %u "$SCRATCH")" != 0 ]] \
         || (( 8#$(stat -c %a "$SCRATCH") & 8#022 )); then
        echo "refusing: $SCRATCH exists but is not a root-owned directory writable only by root" >&2
        exit 1
      fi
    else
      install -d -m 0755 -o root -g root "$SCRATCH"
    fi
    rm -f "$SCRATCH/lab-host" "$SCRATCH/interfaces.allow"
    printf 'lab=veth-dry-run\nowner=%s\n' "${SUDO_USER:-root}" >"$SCRATCH/lab-host"
    printf '%s\n%s\n' "$A" "$B" >"$SCRATCH/interfaces.allow"
    echo "created $A<->$B; scratch interlock files in $SCRATCH"
    echo "run: eval \"\$($0 env)\""
    ;;
  down)
    [[ $EUID -eq 0 ]] || { echo "run as root" >&2; exit 2; }
    ip link del "$A" 2>/dev/null && echo "removed $A<->$B" || echo "$A not present"
    ;;
  status)
    ip -br link show "$A" 2>/dev/null || echo "$A not present"
    ip -br link show "$B" 2>/dev/null || true
    ;;
  env)
    echo "export BNGLOAD_LAB_MARKER=$SCRATCH/lab-host BNGLOAD_INTERFACE_ALLOWLIST=$SCRATCH/interfaces.allow"
    ;;
  *)
    sed -n '2,14p' "$0" | sed 's/^# \{0,1\}//'
    exit 2
    ;;
esac
