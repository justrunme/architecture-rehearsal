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
echo "==> pdb-violation (expect 1 or 3)"
mkdir -p "$OUT/pdb-violation"
set +e
"$BIN" argo-impact --app demo-pdb-violation \
  --desired-dir examples/e2e-argo/pdb-violation/desired \
  --baseline examples/e2e-argo/pdb-violation/baseline.json \
  --out "$OUT/pdb-violation" --quiet | tee "$OUT/pdb-violation/cli.txt"
code=$?
set -e
echo "pdb-violation exit=$code expect=1|3" | tee -a "$OUT/results.txt"
test "$code" -eq 1 -o "$code" -eq 3
test -f "$OUT/pdb-violation/argo-impact.md"
run benign 0

echo "OK e2e_argo_impact"
cat "$OUT/results.txt"
