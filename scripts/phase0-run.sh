#!/usr/bin/env bash
# phase0-run.sh — run the complete Phase 0 measurement set and build the
# acceptance report.
#
#   sudo scripts/phase0-run.sh --tx-if ens2f1 --rx-if ens3f0 --access-if ens2f0 \
#        --spec lab/blaster/accel-ppp-01.toml [--spec lab/blaster/vendor-x.toml] \
#        [--host-name gen-01] [--quick] [--skip-blaster] [--signoff "A. Person"]
#
# --tx-if/--rx-if are two generator ports cabled back-to-back (timestamp
# self-test and I/O ceiling). --access-if (repeatable) adds the DUT-facing
# ports to the health check and host profile. Each --spec describes one DUT
# for the BNG Blaster baselines. Results go to runs/<UTC timestamp>/ and the
# merged host profile to hosts/<host-name>.json.
#
# Every step runs even if an earlier one fails; the acceptance report shows
# what is missing. --quick shortens the long steps for a smoke test; its
# results are not acceptance evidence.
set -uo pipefail

TX=""; RX=""; HOST=$(hostname -s); QUICK=0; SKIP_BLASTER=0; SIGNOFF=""; SPECS=(); ACCESS=()
OUT=""
REPO=$(cd "$(dirname "$0")/.." && pwd)
while [[ $# -gt 0 ]]; do
  case "$1" in
    --tx-if) TX="$2"; shift ;;
    --rx-if) RX="$2"; shift ;;
    --spec) SPECS+=("$2"); shift ;;
    --access-if) ACCESS+=("$2"); shift ;;
    --host-name) HOST="$2"; shift ;;
    --out) OUT="$2"; shift ;;
    --quick) QUICK=1 ;;
    --skip-blaster) SKIP_BLASTER=1 ;;
    --signoff) SIGNOFF="$2"; shift ;;
    -h|--help) sed -n '2,17p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "unknown option $1" >&2; exit 2 ;;
  esac
  shift
done
[[ $EUID -eq 0 ]] || { echo "run as root (sudo)" >&2; exit 2; }
[[ -n "$TX" && -n "$RX" ]] || { echo "--tx-if and --rx-if are required" >&2; exit 2; }

OUT=${OUT:-$REPO/runs/$(date -u +%Y%m%dT%H%M%SZ)}
mkdir -p "$OUT"
LOG="$OUT/phase0-run.log"

# Prefer installed tools, fall back to the build tree.
pick() { command -v "$1" 2>/dev/null || echo "$2"; }
BNGLOAD=$(pick bngload "$REPO/bin/bngload")
AFPROBE=$(pick afprobe "$REPO/engines/afprobe/target/release/afprobe")
if command -v bngload-lab >/dev/null 2>&1; then LABTOOL=(bngload-lab)
else LABTOOL=(env PYTHONPATH="$REPO/tools/lab/src" python3 -m bngload_lab); fi

declare -A RESULT
step() {
  # step NAME CMD... — runs, logs and records the exit code.
  local name=$1; shift
  echo "==> $name: $*" | tee -a "$LOG"
  "$@" 2>&1 | tee -a "$LOG"
  RESULT[$name]=${PIPESTATUS[0]}
  echo "<== $name exit ${RESULT[$name]}" | tee -a "$LOG"
}

if [[ $QUICK -eq 1 ]]; then
  CLOCK_DUR=10s; KB_COUNT=100; AF_RATES=100k,400k,max; AF_DUR=2s; TS04=(--quick); TS01_IT=(--iterations 10)
else
  CLOCK_DUR=300s; KB_COUNT=1000; AF_RATES=50k,100k,200k,400k,800k,1.6M,max; AF_DUR=5s; TS04=(); TS01_IT=()
fi

IFARGS=(--interface "$TX" --interface "$RX")
for a in "${ACCESS[@]}"; do IFARGS+=(--interface "$a"); done
step health      "$BNGLOAD" health "${IFARGS[@]}" -o "$OUT/health.json"
step host        "$BNGLOAD" host profile "${IFARGS[@]}" --name "$HOST" -o "$OUT/host-profile.json"
step clock       "$BNGLOAD" clock check --duration "$CLOCK_DUR" --phc-interface "$TX" -o "$OUT/clock.json"
step tstamp      "$BNGLOAD" tstamp check --tx-if "$TX" --rx-if "$RX" --count 1000 --mode auto -o "$OUT/tstamp.json"
step kbench      "$BNGLOAD" kbench run --scenario all --count "$KB_COUNT" --parent "$TX" -o "$OUT/kbench.json"
step afprobe     "$AFPROBE" run --tx-if "$TX" --rx-if "$RX" --rates "$AF_RATES" --duration "$AF_DUR" -o "$OUT/afprobe.json"

if [[ $SKIP_BLASTER -eq 0 ]]; then
  for spec in "${SPECS[@]}"; do
    dut=$(basename "$spec" .toml)
    step "ts01-$dut"     "${LABTOOL[@]}" ts01 -f "$spec" --out-dir "$OUT" "${TS01_IT[@]}"
    step "baseline-$dut" "${LABTOOL[@]}" baseline -f "$spec" --out-dir "$OUT"
    step "ts04-$dut"     "${LABTOOL[@]}" ts04 -f "$spec" --out-dir "$OUT" "${TS04[@]}"
  done
fi

step open-questions "${LABTOOL[@]}" open-questions "$REPO/docs/phase0/open-questions.toml" -o "$OUT/open-questions.json"
ACC=("${LABTOOL[@]}" acceptance "$OUT" --open-questions "$REPO/docs/phase0/open-questions.toml"
     --out-dir "$OUT" --host-profile-out "$REPO/hosts/$HOST.json")
[[ -n "$SIGNOFF" ]] && ACC+=(--signoff "$SIGNOFF")
step acceptance "${ACC[@]}"

echo
echo "Phase 0 run in $OUT"
for k in "${!RESULT[@]}"; do printf '  %-22s exit %s\n' "$k" "${RESULT[$k]}"; done | sort
echo "Acceptance report: $OUT/acceptance.md"
[[ $QUICK -eq 1 ]] && echo "NOTE: --quick results are a smoke test, not acceptance evidence."
exit "${RESULT[acceptance]:-1}"
