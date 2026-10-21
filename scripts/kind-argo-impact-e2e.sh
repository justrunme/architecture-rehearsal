#!/usr/bin/env bash
# Kind + Argo CD change-impact gate E2E.
# Installs Argo CD, creates a demo Application, then asserts 4 offline-but-identical
# impact scenarios through `rehearsal argo-impact` (no failure masking).
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"
CLUSTER="${KIND_CLUSTER:-argo-impact-e2e}"
ARGO_NS="${ARGO_NS:-argocd}"
OUT_ROOT="${OUT_ROOT:-$ROOT/out/argo-e2e}"
BIN="${BIN:-$ROOT/bin/rehearsal}"

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

echo "==> install Argo CD ($ARGO_NS)"
kubectl create namespace "$ARGO_NS"
kubectl apply -n "$ARGO_NS" -f https://raw.githubusercontent.com/argoproj/argo-cd/v2.14.9/manifests/install.yaml
kubectl -n "$ARGO_NS" wait --for=condition=Available deploy/argocd-server --timeout=300s
kubectl -n "$ARGO_NS" wait --for=condition=Available deploy/argocd-repo-server --timeout=300s
kubectl -n "$ARGO_NS" wait --for=condition=Available deploy/argocd-applicationset-controller --timeout=180s || true
# StatefulSet has no Available condition; wait for readyReplicas via rollout status
kubectl -n "$ARGO_NS" rollout status statefulset/argocd-application-controller --timeout=300s

echo "==> local git repo retained for fixture scenarios (offline compile path)"
REPO_DIR="$OUT_ROOT/git-src"
rm -rf "$REPO_DIR"
mkdir -p "$REPO_DIR/demo"
cp examples/e2e-argo/benign/desired/api.yaml "$REPO_DIR/demo/api.yaml"
(
  cd "$REPO_DIR"
  git init -b main
  git config user.email "e2e@rehearsal.local"
  git config user.name "e2e"
  git add .
  git commit -m "benign demo app"
)

echo "==> assert Argo CD control plane healthy"
kubectl -n "$ARGO_NS" get deploy argocd-server argocd-repo-server -o wide
kubectl -n "$ARGO_NS" get statefulset argocd-application-controller -o wide
READY=$(kubectl -n "$ARGO_NS" get deploy argocd-server -o jsonpath='{.status.readyReplicas}')
test "${READY:-0}" -ge 1
CTRL_READY=$(kubectl -n "$ARGO_NS" get statefulset argocd-application-controller -o jsonpath='{.status.readyReplicas}')
test "${CTRL_READY:-0}" -ge 1

echo "==> create Application that syncs real manifests (guestbook) into kind"
kubectl create namespace demo --dry-run=client -o yaml | kubectl apply -f -
cat <<EOF | kubectl apply -f -
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: demo-benign
  namespace: ${ARGO_NS}
spec:
  project: default
  source:
    repoURL: https://github.com/argoproj/argocd-example-apps.git
    targetRevision: HEAD
    path: guestbook
  destination:
    server: https://kubernetes.default.svc
    namespace: demo
  syncPolicy:
    automated:
      prune: true
      selfHeal: true
    syncOptions:
      - CreateNamespace=true
EOF

echo "==> wait for Application sync"
for i in $(seq 1 90); do
  sync_st=$(kubectl -n "$ARGO_NS" get application demo-benign -o jsonpath='{.status.sync.status}' 2>/dev/null || true)
  health_st=$(kubectl -n "$ARGO_NS" get application demo-benign -o jsonpath='{.status.health.status}' 2>/dev/null || true)
  echo "  sync=$sync_st health=$health_st"
  if [[ "$sync_st" == "Synced" ]]; then
    break
  fi
  if [[ "$i" -eq 15 ]]; then
    kubectl -n "$ARGO_NS" patch application demo-benign --type merge -p '{"operation":{"initiatedBy":{"username":"e2e"},"sync":{"revision":"HEAD"}}}' 2>/dev/null || true
  fi
  sleep 2
done
kubectl -n "$ARGO_NS" get application demo-benign -o yaml | tee "$OUT_ROOT/application-demo-benign.yaml" >/dev/null
SYNC=$(kubectl -n "$ARGO_NS" get application demo-benign -o jsonpath='{.status.sync.status}')
test "$SYNC" = "Synced"
kubectl -n demo get deploy,svc 2>/dev/null | tee "$OUT_ROOT/demo-resources.txt" || true

echo "==> enable admin apiKey, install argocd CLI, obtain auth token for CLIFetcher"
# Patch before port-forward: restarting the server would kill an existing PF.
kubectl -n "$ARGO_NS" patch configmap argocd-cm --type merge -p '{"data":{"accounts.admin":"apiKey, login"}}'
kubectl -n "$ARGO_NS" rollout restart deploy/argocd-server
kubectl -n "$ARGO_NS" rollout status deploy/argocd-server --timeout=180s
ARCH=$(uname -m)
case "$ARCH" in
  x86_64|amd64) ARGO_ARCH=amd64 ;;
  aarch64|arm64) ARGO_ARCH=arm64 ;;
  *) ARGO_ARCH=amd64 ;;
esac
curl -sSL -o /tmp/argocd "https://github.com/argoproj/argo-cd/releases/download/v2.14.9/argocd-linux-${ARGO_ARCH}"
chmod +x /tmp/argocd
sudo mv /tmp/argocd /usr/local/bin/argocd
# Retry port-forward until login works.
PF_PID=""
for attempt in $(seq 1 10); do
  if [[ -n "$PF_PID" ]]; then kill "$PF_PID" 2>/dev/null || true; fi
  kubectl -n "$ARGO_NS" port-forward svc/argocd-server 8080:443 >/tmp/argocd-pf.log 2>&1 &
  PF_PID=$!
  sleep 3
  ARGO_PASS=$(kubectl -n "$ARGO_NS" get secret argocd-initial-admin-secret -o jsonpath='{.data.password}' | base64 -d)
  if argocd login localhost:8080 --username admin --password "$ARGO_PASS" --insecure --grpc-web; then
    break
  fi
  if [[ "$attempt" -eq 10 ]]; then
    echo "argocd login failed after retries" >&2
    cat /tmp/argocd-pf.log >&2 || true
    exit 1
  fi
  sleep 3
done
export ARGOCD_SERVER=localhost:8080
export ARGOCD_INSECURE=true
export ARGOCD_BINARY=argocd
export ARGOCD_GRPC_WEB=true
# Prefer env token (no --auth-token flag) for CLIFetcher.
export ARGOCD_AUTH_TOKEN
ARGOCD_AUTH_TOKEN=$(argocd account generate-token --account admin)
test -n "$ARGOCD_AUTH_TOKEN"
# Smoke: CLIFetcher path resolves manifests for the synced app.
argocd app manifests demo-benign --grpc-web >/tmp/demo-benign-manifests.yaml
test -s /tmp/demo-benign-manifests.yaml

echo "==> native CLIFetcher path: argo-impact --app demo-benign --live"
NATIVE_OUT="$OUT_ROOT/native-benign"
mkdir -p "$NATIVE_OUT"
set +e
"$BIN" argo-impact \
  --app demo-benign \
  --app-namespace "$ARGO_NS" \
  --namespace demo \
  --live-namespace demo \
  --live \
  --cluster kind \
  --out "$NATIVE_OUT" \
  --quiet | tee "$NATIVE_OUT/cli.txt"
native_code=$?
set -e
echo "scenario=native-benign exit=$native_code" | tee -a "$OUT_ROOT/results.txt"
test -f "$NATIVE_OUT/argo-impact.md"
test -f "$NATIVE_OUT/argo-impact.json"
test -f "$NATIVE_OUT/change.json"
grep -q "Gate verdict" "$NATIVE_OUT/argo-impact.md"
# Exact source field from Compile (SourceArgocdCLI); no fallback.
python3 -c 'import json,sys; ch=json.load(open(sys.argv[1])); src=(ch.get("facts") or {}).get("source");
assert src=="argocd-cli", src; print("facts.source="+src)' "$NATIVE_OUT/change.json"
# Benign Synced guestbook: desired manifests from Argo equal live demo workloads, so the
# change envelope has no replica/netpol/pdb mutations. All detectors PASS and analyze
# approves → gate PASS → exit 0. (BLOCK/WARN/UNKNOWN would mean drift or missing inventory.)
test "$native_code" -eq 0

echo "==> native risky Application (OutOfSync desired; do not apply)"
REPO_URL="${ARGO_E2E_REPO_URL:-https://github.com/justrunme/architecture-rehearsal.git}"
REPO_REV="${ARGO_E2E_REPO_REVISION:-${GITHUB_SHA:-$(git rev-parse HEAD)}}"
echo "  repo=$REPO_URL rev=$REPO_REV path=examples/e2e-argo/native-risky/desired"
kubectl create namespace risky --dry-run=client -o yaml | kubectl apply -f -
cat <<EOF | kubectl apply -f -
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: demo-risky
  namespace: ${ARGO_NS}
spec:
  project: default
  source:
    repoURL: ${REPO_URL}
    targetRevision: ${REPO_REV}
    path: examples/e2e-argo/native-risky/desired
  destination:
    server: https://kubernetes.default.svc
    namespace: risky
  # Manual sync only: desired is intentionally unschedulable; never apply to the cluster.
  syncPolicy: {}
EOF
# Wait until Argo can render desired manifests (repo cloned), not until Synced.
for i in $(seq 1 90); do
  if argocd app manifests demo-risky --grpc-web >/tmp/demo-risky-manifests.yaml 2>/tmp/demo-risky-manifests.err; then
    if grep -q 'kind: Deployment' /tmp/demo-risky-manifests.yaml; then
      echo "  manifests ready (attempt $i)"
      break
    fi
  fi
  if [[ "$i" -eq 90 ]]; then
    echo "demo-risky manifests not ready" >&2
    cat /tmp/demo-risky-manifests.err >&2 || true
    kubectl -n "$ARGO_NS" get application demo-risky -o yaml | tee "$OUT_ROOT/application-demo-risky.yaml" >&2 || true
    exit 1
  fi
  sleep 2
done
test -s /tmp/demo-risky-manifests.yaml
grep -q 'no-such-node' /tmp/demo-risky-manifests.yaml

RISKY_OUT="$OUT_ROOT/native-risky"
mkdir -p "$RISKY_OUT"
set +e
"$BIN" argo-impact \
  --app demo-risky \
  --app-namespace "$ARGO_NS" \
  --namespace risky \
  --live-namespace risky \
  --live \
  --cluster kind \
  --out "$RISKY_OUT" \
  --quiet | tee "$RISKY_OUT/cli.txt"
risky_code=$?
set -e
echo "scenario=native-risky exit=$risky_code" | tee -a "$OUT_ROOT/results.txt"
test -f "$RISKY_OUT/change.json"
python3 -c 'import json,sys; ch=json.load(open(sys.argv[1])); src=(ch.get("facts") or {}).get("source");
assert src=="argocd-cli", src; print("facts.source="+src)' "$RISKY_OUT/change.json"
grep -q "Gate verdict" "$RISKY_OUT/argo-impact.md"
# Desired heavy-api uses a nodeSelector that matches zero kind nodes while live namespace
# risky is empty → scheduling-capacity BLOCK. Aggregate prefers BLOCK over UNKNOWN.
# Expect exit 3.
test "$risky_code" -eq 3
grep -q '"gateVerdict": "BLOCK"' "$RISKY_OUT/argo-impact.json"

kill "$PF_PID" 2>/dev/null || true

run_scenario() {
  local name="$1"
  local expect="$2"
  local dir="examples/e2e-argo/$name"
  local out="$OUT_ROOT/$name"
  mkdir -p "$out"
  echo "==> scenario $name (expect exit $expect)"
  set +e
  "$BIN" argo-impact \
    --app "demo-$name" \
    --desired-dir "$dir/desired" \
    --baseline "$dir/baseline.json" \
    --out "$out" \
    --quiet | tee "$out/cli.txt"
  local code=$?
  set -e
  echo "scenario=$name exit=$code expect=$expect" | tee -a "$OUT_ROOT/results.txt"
  test "$code" -eq "$expect"
  test -f "$out/argo-impact.md"
  test -f "$out/argo-impact.json"
  # No masking: fail if markdown lacks gate verdict
  grep -q "Gate verdict" "$out/argo-impact.md"
}

# unschedulable replicas -> BLOCK (3)
run_scenario unschedulable 3
# NetworkPolicy cutting DB dependency -> BLOCK (3)
run_scenario netpol-cut 3
# PDB violation -> BLOCK (3)
run_scenario pdb-violation 3
# UNKNOWN when PDB inventory missing -> exit 4
run_scenario unknown-pdb-inventory 4
# benign change -> PASS (0)
run_scenario benign 0

echo "==> collect CI-facing summary artifact"
{
  echo "# Argo CD impact gate — kind e2e results"
  echo
  echo "Argo CD namespace: \`$ARGO_NS\`"
  echo
  echo '```'
  cat "$OUT_ROOT/results.txt"
  echo '```'
  echo
  for name in native-benign native-risky unschedulable netpol-cut pdb-violation unknown-pdb-inventory benign; do
    echo "## $name"
    echo
    if [[ -f "$OUT_ROOT/$name/cli.txt" ]]; then
      echo '```'
      cat "$OUT_ROOT/$name/cli.txt"
      echo '```'
      echo
    fi
    if [[ -f "$OUT_ROOT/$name/argo-impact.md" ]]; then
      # Trim generated timestamp line for stable docs paste; keep verdicts.
      grep -E '^\- \*\*|Gate verdict|^## |^\| `|gate=' "$OUT_ROOT/$name/argo-impact.md" | head -40
      echo
    fi
  done
} | tee "$OUT_ROOT/SUMMARY.md"

echo "OK kind-argo-impact-e2e"
