#!/usr/bin/env bash
# host-prep.sh — prepare a bare-metal Ubuntu host for bngload Phase 0.
#
# Default is a DRY RUN: every change is printed, nothing is modified.
# Re-run with --apply to make the changes. Each setting maps to a check in
# `bngload health`, so a clean health report follows a successful --apply.
#
#   sudo scripts/host-prep.sh --interface ens2f0 --interface ens2f1 --qinq
#   sudo scripts/host-prep.sh --interface ens2f0 --interface ens2f1 --qinq --apply
#
# Marking the machine as an authorised lab host is a separate, explicit act:
#
#   sudo scripts/host-prep.sh --mark-lab-host --lab bng-lab-1 --owner "Telco Cloud" \
#        --interface ens2f0 --interface ens2f1 --apply
#
# What it does (design section 9 host tuning, MR-2, MR-6, SR-11, SR-19):
#   sysctl     net.core.{rmem,wmem}_max = 64 MiB, netdev_max_backlog = 250000
#   limits     nofile 1048576 for root
#   chrony     maxslewrate 100 and makestep only for the first 3 updates
#   per port   IPv6 disabled (no RS/ND/MLD noise), RX VLAN offload off,
#              RX coalescing off, RX/TX rings at maximum, GRO/LRO off,
#              MTU 1508 with --qinq; NO IP address is ever added or removed
#   --persist  a systemd unit re-applies the per-port settings at boot
#   --mark-lab-host  writes /etc/bngload/lab-host and interfaces.allow
set -euo pipefail

APPLY=0
PERSIST=0
QINQ=0
MARK=0
LAB=""
OWNER=""
IFACES=()

usage() { sed -n '2,30p' "$0" | sed 's/^# \{0,1\}//'; exit "${1:-0}"; }

while [[ $# -gt 0 ]]; do
  case "$1" in
    --apply) APPLY=1 ;;
    --persist) PERSIST=1 ;;
    --qinq) QINQ=1 ;;
    --interface|-i) IFACES+=("$2"); shift ;;
    --mark-lab-host) MARK=1 ;;
    --lab) LAB="$2"; shift ;;
    --owner) OWNER="$2"; shift ;;
    -h|--help) usage 0 ;;
    *) echo "unknown option $1" >&2; usage 2 ;;
  esac
  shift
done

[[ $EUID -eq 0 ]] || { echo "run as root (sudo)" >&2; exit 2; }
[[ ${#IFACES[@]} -gt 0 ]] || { echo "at least one --interface is required" >&2; exit 2; }

MODE=$([[ $APPLY -eq 1 ]] && echo apply || echo plan)
run() {
  # Prints the command; executes it only with --apply. Failures are reported,
  # not fatal: some drivers reject individual ethtool settings.
  echo "[$MODE] $*"
  if [[ $APPLY -eq 1 ]]; then
    if ! "$@"; then echo "  ! failed (continuing): $*" >&2; fi
  fi
}
write_file() {
  # write_file PATH CONTENT
  echo "[$MODE] write $1"
  sed 's/^/    | /' <<<"$2"
  if [[ $APPLY -eq 1 ]]; then
    install -d -m 0755 "$(dirname "$1")"
    printf '%s\n' "$2" >"$1"
  fi
}

for ifc in "${IFACES[@]}"; do
  [[ -e /sys/class/net/$ifc ]] || { echo "interface $ifc does not exist" >&2; exit 2; }
  addrs=$(ip -o addr show dev "$ifc" | awk '$3=="inet" || ($3=="inet6" && $4 !~ /^fe80:/) {print $4}' | tr '\n' ' ') || true
  if [[ -n "$addrs" ]]; then
    echo "REFUSING: $ifc carries routable addresses ($addrs). A test port must have no IP address (SR-19)." >&2
    echo "Remove them yourself (and from netplan) after confirming this port is not in use." >&2
    exit 1
  fi
done

echo "== kernel network buffers (design 9)"
write_file /etc/sysctl.d/90-bngload.conf "# bngload Phase 0 (scripts/host-prep.sh)
net.core.rmem_max = 67108864
net.core.wmem_max = 67108864
net.core.netdev_max_backlog = 250000"
run sysctl -q -p /etc/sysctl.d/90-bngload.conf

echo "== file descriptor limit"
write_file /etc/security/limits.d/90-bngload.conf "# bngload Phase 0: kbench and per-session sockets
root soft nofile 1048576
root hard nofile 1048576"

echo "== chrony (MR-2: slew only during runs)"
if [[ -d /etc/chrony ]]; then
  write_file /etc/chrony/conf.d/bngload.conf "# bngload MR-2: bounded slew, steps only right after boot
maxslewrate 100
makestep 1 3"
  if grep -Eq '^\s*makestep\s+\S+\s+-1' /etc/chrony/chrony.conf 2>/dev/null; then
    echo "  ! /etc/chrony/chrony.conf has 'makestep … -1' (step at any time); change it to 'makestep 1 3'" >&2
  fi
  if ! grep -Eq '^\s*(confdir|include)\s+/etc/chrony/conf.d' /etc/chrony/chrony.conf 2>/dev/null; then
    echo "  ! /etc/chrony/chrony.conf does not read /etc/chrony/conf.d; add: confdir /etc/chrony/conf.d" >&2
  fi
  run systemctl restart chrony
else
  echo "  ! chrony not installed: sudo apt install chrony (scripts/install-deps-ubuntu.sh)" >&2
fi

prep_port() {
  local ifc=$1
  echo "== test port $ifc"
  run sysctl -q -w "net.ipv6.conf.${ifc}.disable_ipv6=1"
  run ethtool -K "$ifc" rxvlan off
  if ! ethtool -k "$ifc" 2>/dev/null | grep -q '^rx-vlan-stag-hw-parse: off \[fixed\]'; then
    run ethtool -K "$ifc" rx-vlan-stag-hw-parse off
  fi
  run ethtool -K "$ifc" gro off lro off
  run ethtool -C "$ifc" adaptive-rx off rx-usecs 0
  local rxmax txmax
  rxmax=$(ethtool -g "$ifc" 2>/dev/null | awk '/^Pre-set/{p=1} p&&/^RX:/{print $2; exit}') || true
  txmax=$(ethtool -g "$ifc" 2>/dev/null | awk '/^Pre-set/{p=1} p&&/^TX:/{print $2; exit}') || true
  if [[ "$rxmax" =~ ^[0-9]+$ && "$txmax" =~ ^[0-9]+$ ]]; then
    run ethtool -G "$ifc" rx "$rxmax" tx "$txmax"
  fi
  if [[ $QINQ -eq 1 ]]; then
    local mtu; mtu=$(cat "/sys/class/net/$ifc/mtu")
    (( mtu >= 1508 )) || run ip link set dev "$ifc" mtu 1508
  fi
  run ip link set dev "$ifc" up
}
for ifc in "${IFACES[@]}"; do prep_port "$ifc"; done

if [[ $PERSIST -eq 1 ]]; then
  echo "== persist per-port settings at boot"
  args="--apply"
  [[ $QINQ -eq 1 ]] && args+=" --qinq"
  for ifc in "${IFACES[@]}"; do args+=" --interface $ifc"; done
  write_file /usr/local/sbin/bngload-host-prep "$(cat "$0")"
  run chmod 0755 /usr/local/sbin/bngload-host-prep
  write_file /etc/systemd/system/bngload-host-prep.service "[Unit]
Description=bngload test-port preparation (scripts/host-prep.sh)
After=network-online.target chrony.service
Wants=network-online.target

[Service]
Type=oneshot
ExecStart=/usr/local/sbin/bngload-host-prep $args
RemainAfterExit=yes

[Install]
WantedBy=multi-user.target"
  run systemctl daemon-reload
  run systemctl enable bngload-host-prep.service
fi

if [[ $MARK -eq 1 ]]; then
  echo "== lab marker and interface allowlist (SR-19, SR-11)"
  [[ -n "$LAB" && -n "$OWNER" ]] || { echo "--mark-lab-host needs --lab and --owner" >&2; exit 2; }
  write_file /etc/bngload/lab-host "# This host is an authorised bngload lab generator.
# Created by scripts/host-prep.sh on $(date -u +%Y-%m-%dT%H:%M:%SZ) by ${SUDO_USER:-root}.
lab=$LAB
owner=$OWNER"
  write_file /etc/bngload/interfaces.allow "# Interfaces bngload tools may transmit on (one per line).
$(printf '%s\n' "${IFACES[@]}")"
fi

echo
if [[ $APPLY -eq 1 ]]; then
  echo "Done. Verify with: sudo bngload health $(printf -- '--interface %s ' "${IFACES[@]}")"
else
  echo "Dry run only. Re-run with --apply to make these changes."
fi
echo "Not changed here (do by hand if wanted): CPU isolation (isolcpus/nohz_full on the kernel"
echo "command line), irqbalance exclusion for the test NICs, BIOS C-states and turbo."
