#!/usr/bin/env bash
# install-bngblaster.sh — install BNG Blaster (rtbrick/bngblaster) for the
# Phase 0 baseline runs.
#
#   sudo scripts/install-bngblaster.sh                  # release .deb (default)
#   sudo scripts/install-bngblaster.sh --version 0.9.37
#   sudo scripts/install-bngblaster.sh --source         # build from git
#
# The .deb installs /usr/sbin/bngblaster; a source build installs
# /usr/local/sbin/bngblaster. bngload-lab finds either automatically.
# The script prints the SHA-256 of what it installed so the host profile can
# record exactly which build produced the baseline numbers.
set -euo pipefail

VERSION=${BNGBLASTER_VERSION:-0.9.37}
LIBDICT_VERSION=${LIBDICT_VERSION:-1.0.6}
MODE=deb

while [[ $# -gt 0 ]]; do
  case "$1" in
    --version) VERSION="$2"; shift ;;
    --source) MODE=source ;;
    -h|--help) sed -n '2,13p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "unknown option $1" >&2; exit 2 ;;
  esac
  shift
done

[[ $EUID -eq 0 ]] || { echo "run as root (sudo)" >&2; exit 2; }
. /etc/os-release
[[ "$ID" == "ubuntu" ]] || echo "warning: tested on Ubuntu 22.04/24.04, this is $PRETTY_NAME" >&2
ARCH=$(dpkg --print-architecture)
[[ "$ARCH" == "amd64" ]] || { echo "BNG Blaster release packages are amd64 only; use --source" >&2; [[ $MODE == source ]] || exit 2; }

WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT
export DEBIAN_FRONTEND=noninteractive

if [[ $MODE == deb ]]; then
  apt-get update -q
  apt-get install -y -q curl ca-certificates
  DEB="bngblaster-${VERSION}-ubuntu-${VERSION_ID}_amd64.deb"
  URL="https://github.com/rtbrick/bngblaster/releases/download/${VERSION}/${DEB}"
  echo "downloading $URL"
  curl -fL --retry 3 -o "$WORK/$DEB" "$URL"
  sha256sum "$WORK/$DEB"
  # apt resolves the package's runtime dependencies (libssl, ncurses, jansson, pcap)
  apt-get install -y -q "$WORK/$DEB"
  BIN=/usr/sbin/bngblaster
else
  apt-get update -q
  apt-get install -y -q git cmake build-essential curl unzip ca-certificates \
    libpcap-dev libcunit1-dev libncurses-dev libssl-dev libjansson-dev libnuma-dev
  echo "building libdict ${LIBDICT_VERSION} (static, required by BNG Blaster)"
  git clone -q --depth 1 --branch "${LIBDICT_VERSION}" https://github.com/rtbrick/libdict.git "$WORK/libdict"
  cmake -S "$WORK/libdict" -B "$WORK/libdict/build" -DCMAKE_BUILD_TYPE=Release -DLIBDICT_TESTS=OFF -DLIBDICT_TOOLS=OFF >/dev/null
  make -C "$WORK/libdict/build" -j"$(nproc)" >/dev/null
  make -C "$WORK/libdict/build" install >/dev/null
  echo "building bngblaster ${VERSION}"
  git clone -q --depth 1 --branch "${VERSION}" https://github.com/rtbrick/bngblaster.git "$WORK/bngblaster"
  (cd "$WORK/bngblaster" && cmake -B build -DCMAKE_BUILD_TYPE=RelWithDebInfo \
      -DBNGBLASTER_VERSION="${VERSION}" -DGIT_REF="${VERSION}" -DGIT_SHA="$(git rev-parse HEAD)" >/dev/null)
  make -C "$WORK/bngblaster/build" -j"$(nproc)" >/dev/null
  make -C "$WORK/bngblaster/build" install >/dev/null
  BIN=$(command -v bngblaster || echo /usr/local/sbin/bngblaster)
fi

echo
"$BIN" -v | head -6
sha256sum "$BIN"
echo "installed $BIN"
