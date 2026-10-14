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
      sysctl -q -w "net.ipv6.conf.$i.disable_ipv6=1"
      ip link set dev "$i" mtu 1508 up
    done
    install -d -m 0755 "$SCRATCH"
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
