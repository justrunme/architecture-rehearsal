#!/usr/bin/env bash
# Fail-closed demo runner: expects block (exit 3) for each golden scenario.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"
BIN="${BIN:-bin/rehearsal}"
if [[ ! -x "$BIN" ]]; then
  go build -o "$BIN" ./cmd/rehearsal
fi

run_expect_block() {
  local name="$1" baseline="$2" change="$3" html="$4"
  echo "==> $name"
  set +e
  "$BIN" analyze --baseline "$baseline" --change "$change" --out out --html "$html" --quiet
  code=$?
  set -e
  if [[ "$code" -ne 3 ]]; then
    echo "error: $name expected exit 3 (block), got $code" >&2
    exit 1
  fi
}

run_expect_block "rwo-node-loss" \
  examples/golden/rwo-node-loss/baseline.json \
  examples/golden/rwo-node-loss/change.json \
  out/rwo-report.html

run_expect_block "cni-ip-capacity" \
  examples/golden/cni-ip-capacity/baseline.json \
  examples/golden/cni-ip-capacity/change.json \
  out/cni-report.html

run_expect_block "prom-zero-match" \
  examples/golden/prom-zero-match/baseline.json \
  examples/golden/prom-zero-match/change.json \
  out/prom-report.html

echo "demo OK (3/3 block as expected)"
