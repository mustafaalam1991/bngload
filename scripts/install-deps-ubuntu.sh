#!/usr/bin/env bash
# install-deps-ubuntu.sh — build and runtime dependencies for bngload Phase 0
# on Ubuntu 22.04 or 24.04 (bare metal).
#
#   sudo scripts/install-deps-ubuntu.sh            # apt packages, Go, Rust
#   sudo scripts/install-deps-ubuntu.sh --no-rust  # skip rustup
#
# Installs:
#   apt     build-essential git curl jq iproute2 ethtool chrony linuxptp
#           tcpdump python3 (+ python3-tomli on 22.04) shellcheck
#   Go      official tarball into /usr/local/go (Ubuntu's golang is too old
#           on 22.04); SHA-256 checked against go.dev
#   Rust    rustup, stable toolchain, for the invoking user ($SUDO_USER)
#
# BNG Blaster has its own script: scripts/install-bngblaster.sh
set -euo pipefail

GO_VERSION=${GO_VERSION:-1.24.7}
WITH_GO=1
WITH_RUST=1
while [[ $# -gt 0 ]]; do
  case "$1" in
    --no-go) WITH_GO=0 ;;
    --no-rust) WITH_RUST=0 ;;
    --go-version) GO_VERSION="$2"; shift ;;
    -h|--help) sed -n '2,17p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "unknown option $1" >&2; exit 2 ;;
  esac
  shift
done

[[ $EUID -eq 0 ]] || { echo "run as root (sudo)" >&2; exit 2; }
# Read os-release in a subshell so its variables (VERSION, ID, …) cannot
# clobber this script's.
OS_VERSION_ID=$(. /etc/os-release && echo "$VERSION_ID")
case "$OS_VERSION_ID" in
  22.04|24.04) ;;
  *) echo "warning: tested on Ubuntu 22.04 and 24.04; this is $(. /etc/os-release && echo "$PRETTY_NAME")" >&2 ;;
esac

export DEBIAN_FRONTEND=noninteractive
PKGS=(build-essential git curl ca-certificates jq make iproute2 ethtool chrony linuxptp tcpdump
      python3 python3-venv python3-pip shellcheck)
[[ "$OS_VERSION_ID" == "22.04" ]] && PKGS+=(python3-tomli)   # tomllib arrived in Python 3.11
apt-get update -q
apt-get install -y -q "${PKGS[@]}"

go_ok() {
  command -v go >/dev/null 2>&1 || [[ -x /usr/local/go/bin/go ]] || return 1
  local v; v=$( (command -v go >/dev/null && go env GOVERSION) || /usr/local/go/bin/go env GOVERSION)
  v=${v#go}
  [[ "$(printf '%s\n1.22\n' "${v%.*}" | sort -V | head -1)" == "1.22" ]]
}

if [[ $WITH_GO -eq 1 ]]; then
  if go_ok; then
    echo "Go already installed: $( (command -v go >/dev/null && go version) || /usr/local/go/bin/go version)"
  else
    ARCH=$(dpkg --print-architecture)   # amd64 | arm64
    TGZ="go${GO_VERSION}.linux-${ARCH}.tar.gz"
    WORK=$(mktemp -d); trap 'rm -rf "$WORK"' EXIT
    echo "installing Go ${GO_VERSION}"
    curl -fsSL --retry 3 -o "$WORK/$TGZ" "https://go.dev/dl/$TGZ"
    WANT=$(curl -fsSL "https://go.dev/dl/?mode=json&include=all" | python3 -c '
import json, sys
name = sys.argv[1]
for rel in json.load(sys.stdin):
    for f in rel["files"]:
        if f["filename"] == name:
            print(f["sha256"]); sys.exit(0)
sys.exit(1)' "$TGZ") || { echo "cannot find checksum for $TGZ on go.dev" >&2; exit 1; }
    echo "$WANT  $WORK/$TGZ" | sha256sum -c -
    rm -rf /usr/local/go
    tar -C /usr/local -xzf "$WORK/$TGZ"
    echo 'export PATH=$PATH:/usr/local/go/bin' >/etc/profile.d/go.sh
    echo "Go installed in /usr/local/go (new shells get it on PATH via /etc/profile.d/go.sh)"
  fi
fi

if [[ $WITH_RUST -eq 1 ]]; then
  TARGET_USER=${SUDO_USER:-root}
  if sudo -u "$TARGET_USER" -H bash -lc 'command -v cargo' >/dev/null 2>&1; then
    echo "Rust already installed for $TARGET_USER: $(sudo -u "$TARGET_USER" -H bash -lc 'cargo --version')"
  else
    echo "installing Rust (rustup, stable) for $TARGET_USER"
    sudo -u "$TARGET_USER" -H bash -c \
      'curl --proto "=https" --tlsv1.2 -fsSL https://sh.rustup.rs | sh -s -- -y --profile minimal --default-toolchain stable -c clippy'
  fi
fi

echo
echo "Next: scripts/install-bngblaster.sh, then 'make' and 'sudo make install'."
