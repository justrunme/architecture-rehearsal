#!/usr/bin/env bash
# Kind e2e: pdb-rollout template compare via --live (server-side dry-run).
# Asserts identical desired vs live is WARN/PASS (not BLOCK), and an env change is BLOCK.
# This must fail on hash-based default-stripping (httpGet.scheme false positives).
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"
CLUSTER="${KIND_CLUSTER:-rollout-template-e2e}"
OUT_ROOT="${OUT_ROOT:-$ROOT/out/rollout-template-e2e}"
BIN="${BIN:-$ROOT/bin/rehearsal}"
SAME_DIR="$ROOT/examples/e2e-argo/rollout-template-live"
ENV_DIR="$ROOT/examples/e2e-argo/rollout-template-live-env"

need() { command -v "$1" >/dev/null || { echo "missing required tool: $1"; exit 1; }; }
need kind
need kubectl
need docker

cleanup() {
  kind delete cluster --name "$CLUSTER" 2>/dev/null || true
}
trap cleanup EXIT

mkdir -p "$OUT_ROOT"
echo "==> build rehearsal"
go build -o "$BIN" ./cmd/rehearsal

echo "==> kind create $CLUSTER"
kind create cluster --name "$CLUSTER"

echo "==> apply live baseline (probes + configMap volume + resources + dangerous strategy)"
# Pull may fail in sandboxes without registry access; retry via kind load if needed.
IMG=nginxinc/nginx-unprivileged:1.27-alpine
if ! docker image inspect "$IMG" >/dev/null 2>&1; then
  docker pull "$IMG" || true
fi
kind load docker-image "$IMG" --name "$CLUSTER" 2>/dev/null || true
kubectl apply -f "$SAME_DIR/app.yaml"
kubectl -n shop rollout status deploy/api --timeout=180s

pdb_status() {
  local out="$1"
  python3 - "$out" <<'PY'
import json, sys
d=json.load(open(sys.argv[1]))
for det in d.get("detectors", []):
    if det.get("detector") == "pdb-rollout":
        print(det.get("status", ""))
        sys.exit(0)
print("MISSING")
sys.exit(1)
PY
}

echo "==> argo-impact --live with IDENTICAL desired (expect pdb-rollout WARN or PASS, not BLOCK)"
SAME_OUT="$OUT_ROOT/identical"
mkdir -p "$SAME_OUT"
set +e
"$BIN" argo-impact --live --desired-dir "$SAME_DIR" --namespace shop --app shop \
  --out "$SAME_OUT" --quiet | tee "$SAME_OUT/cli.txt"
code=$?
set -e
st=$(pdb_status "$SAME_OUT/argo-impact.json")
echo "identical pdb-rollout status=$st exit=$code"
case "$st" in
  WARN|PASS) ;;
  *)
    echo "FAIL: identical desired must not BLOCK pdb-rollout (got $st)"
    echo "--- report ---"
    cat "$SAME_OUT/argo-impact.md" || true
    exit 1
    ;;
esac

echo "==> argo-impact --live with ENV change (expect pdb-rollout BLOCK)"
ENV_OUT="$OUT_ROOT/env-change"
mkdir -p "$ENV_OUT"
set +e
"$BIN" argo-impact --live --desired-dir "$ENV_DIR" --namespace shop --app shop \
  --out "$ENV_OUT" --quiet | tee "$ENV_OUT/cli.txt"
code=$?
set -e
st=$(pdb_status "$ENV_OUT/argo-impact.json")
echo "env-change pdb-rollout status=$st exit=$code"
test "$st" = "BLOCK"
test "$code" -eq 3

echo "OK kind-rollout-template-e2e"
echo "identical=$SAME_OUT env=$ENV_OUT" | tee "$OUT_ROOT/results.txt"
