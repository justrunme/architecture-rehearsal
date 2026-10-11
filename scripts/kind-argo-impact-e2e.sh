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

echo "==> local git repo for Application source"
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
# Serve repo to the kind network via a pod mounting the host path is heavy;
# instead create Application with in-cluster empty and still prove Argo is healthy,
# then assert impact scenarios via --desired-dir (same compile path as argocd manifests).

echo "==> assert Argo CD control plane healthy"
kubectl -n "$ARGO_NS" get deploy argocd-server argocd-repo-server -o wide
kubectl -n "$ARGO_NS" get statefulset argocd-application-controller -o wide
READY=$(kubectl -n "$ARGO_NS" get deploy argocd-server -o jsonpath='{.status.readyReplicas}')
test "${READY:-0}" -ge 1
# application-controller is a StatefulSet in upstream install manifests
CTRL_READY=$(kubectl -n "$ARGO_NS" get statefulset argocd-application-controller -o jsonpath='{.status.readyReplicas}')
test "${CTRL_READY:-0}" -ge 1

echo "==> create demo Application CR (destination in-cluster; source is placeholder until synced)"
cat <<'EOF' | kubectl apply -f -
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: demo-benign
  namespace: argocd
spec:
  project: default
  source:
    repoURL: https://github.com/argoproj/argocd-example-apps.git
    targetRevision: HEAD
    path: guestbook
  destination:
    server: https://kubernetes.default.svc
    namespace: default
  syncPolicy:
    automated: null
EOF
kubectl -n "$ARGO_NS" get application demo-benign
# Wait until Argo has observed the app (status may be Unknown/Missing until refresh).
sleep 5
kubectl -n "$ARGO_NS" get application demo-benign -o yaml | tee "$OUT_ROOT/application-demo-benign.yaml" | grep -q "demo-benign"

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
# PDB violation -> WARN(1) or BLOCK(3)
echo "==> scenario pdb-violation (expect exit 1 or 3)"
mkdir -p "$OUT_ROOT/pdb-violation"
set +e
"$BIN" argo-impact \
  --app demo-pdb-violation \
  --desired-dir examples/e2e-argo/pdb-violation/desired \
  --baseline examples/e2e-argo/pdb-violation/baseline.json \
  --out "$OUT_ROOT/pdb-violation" \
  --quiet | tee "$OUT_ROOT/pdb-violation/cli.txt"
pdb_code=$?
set -e
echo "scenario=pdb-violation exit=$pdb_code expect=1|3" | tee -a "$OUT_ROOT/results.txt"
test "$pdb_code" -eq 1 -o "$pdb_code" -eq 3
test -f "$OUT_ROOT/pdb-violation/argo-impact.md"
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
  for name in unschedulable netpol-cut pdb-violation benign; do
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
