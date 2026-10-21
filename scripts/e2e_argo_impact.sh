#!/usr/bin/env bash
# Offline Argo impact iron path (no Kind). Asserts 4 scenarios; no failure masking.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"
BIN="${BIN:-$ROOT/bin/rehearsal}"
OUT="${OUT:-$ROOT/out/argo-impact-e2e}"

if [[ ! -x "$BIN" ]]; then
  go build -o "$BIN" ./cmd/rehearsal
fi
mkdir -p "$OUT"
: > "$OUT/results.txt"

run() {
  local name="$1" expect="$2"
  local dir="examples/e2e-argo/$name"
  local o="$OUT/$name"
  mkdir -p "$o"
  echo "==> $name (expect $expect)"
  set +e
  "$BIN" argo-impact --app "demo-$name" --desired-dir "$dir/desired" \
    --baseline "$dir/baseline.json" --out "$o" --quiet | tee "$o/cli.txt"
  local code=$?
  set -e
  echo "$name exit=$code expect=$expect" | tee -a "$OUT/results.txt"
  test "$code" -eq "$expect"
  test -f "$o/argo-impact.json"
  test -f "$o/argo-impact.md"
  grep -q "Gate verdict" "$o/argo-impact.md"
}

run unschedulable 3
run netpol-cut 3
run pdb-violation 3
run unknown-pdb-inventory 4
run benign 0
# C5 / controls from FEASIBILITY: dangerous maxUnavailable BLOCK; maxUnavailable:1 PASS; no-PDB zero-avail BLOCK
run c5-rollout-maxunavail 3
run fp3-maxunavail-one 0
run fp4-zero-avail-no-pdb 3

echo "OK e2e_argo_impact"
cat "$OUT/results.txt"
