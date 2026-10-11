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
  git update-server-info
)

echo "==> assert Argo CD control plane healthy"
kubectl -n "$ARGO_NS" get deploy argocd-server argocd-repo-server -o wide
kubectl -n "$ARGO_NS" get statefulset argocd-application-controller -o wide
READY=$(kubectl -n "$ARGO_NS" get deploy argocd-server -o jsonpath='{.status.readyReplicas}')
test "${READY:-0}" -ge 1
CTRL_READY=$(kubectl -n "$ARGO_NS" get statefulset argocd-application-controller -o jsonpath='{.status.readyReplicas}')
test "${CTRL_READY:-0}" -ge 1

echo "==> in-cluster git HTTP server for native Application sync"
kubectl create namespace demo --dry-run=client -o yaml | kubectl apply -f -
BARE="$OUT_ROOT/git-src.git"
rm -rf "$BARE"
git clone --bare "$REPO_DIR" "$BARE"
git --git-dir="$BARE" update-server-info
# Ensure dumb HTTP info files are present for nginx static serve.
test -f "$BARE/HEAD"
test -f "$BARE/info/refs" || git --git-dir="$BARE" update-server-info

cat <<'EOF' | kubectl apply -f -
apiVersion: v1
kind: Pod
metadata:
  name: git-http
  namespace: argocd
  labels:
    app: git-http
spec:
  containers:
    - name: git
      image: nginx:1.27-alpine
      ports:
        - containerPort: 80
      volumeMounts:
        - name: www
          mountPath: /usr/share/nginx/html
      readinessProbe:
        tcpSocket:
          port: 80
        initialDelaySeconds: 1
        periodSeconds: 2
  volumes:
    - name: www
      emptyDir: {}
---
apiVersion: v1
kind: Service
metadata:
  name: git-http
  namespace: argocd
spec:
  selector:
    app: git-http
  ports:
    - port: 80
      targetPort: 80
EOF
kubectl -n "$ARGO_NS" wait --for=condition=Ready pod/git-http --timeout=120s
# Seed bare repo into the pod (avoids ConfigMap size / init apk issues).
kubectl -n "$ARGO_NS" cp "$BARE" "git-http:/usr/share/nginx/html/git-src.git"
kubectl -n "$ARGO_NS" exec git-http -- ls /usr/share/nginx/html/git-src.git/HEAD

# Allow insecure HTTP repo in Argo CD
kubectl -n "$ARGO_NS" patch configmap argocd-cm --type merge -p '{"data":{"timeout.reconciliation":"30s"}}' || true
cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: Secret
metadata:
  name: git-http-repo
  namespace: ${ARGO_NS}
  labels:
    argocd.argoproj.io/secret-type: repository
stringData:
  type: git
  url: http://git-http.${ARGO_NS}.svc.cluster.local/git-src.git
  insecure: "true"
EOF

echo "==> create demo Application CR syncing in-cluster git repo"
cat <<EOF | kubectl apply -f -
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: demo-benign
  namespace: ${ARGO_NS}
spec:
  project: default
  source:
    repoURL: http://git-http.${ARGO_NS}.svc.cluster.local/git-src.git
    targetRevision: HEAD
    path: demo
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
for _ in $(seq 1 90); do
  sync=$(kubectl -n "$ARGO_NS" get application demo-benign -o jsonpath='{.status.sync.status}' 2>/dev/null || true)
  health=$(kubectl -n "$ARGO_NS" get application demo-benign -o jsonpath='{.status.health.status}' 2>/dev/null || true)
  echo "  sync=$sync health=$health"
  if [[ "$sync" == "Synced" ]]; then
    break
  fi
  sleep 2
done
kubectl -n "$ARGO_NS" get application demo-benign -o yaml | tee "$OUT_ROOT/application-demo-benign.yaml" >/dev/null
SYNC=$(kubectl -n "$ARGO_NS" get application demo-benign -o jsonpath='{.status.sync.status}')
test "$SYNC" = "Synced"

echo "==> install argocd CLI + obtain auth token for CLIFetcher"
ARCH=$(uname -m)
case "$ARCH" in
  x86_64|amd64) ARGO_ARCH=amd64 ;;
  aarch64|arm64) ARGO_ARCH=arm64 ;;
  *) ARGO_ARCH=amd64 ;;
esac
curl -sSL -o /tmp/argocd "https://github.com/argoproj/argo-cd/releases/download/v2.14.9/argocd-linux-${ARGO_ARCH}"
chmod +x /tmp/argocd
sudo mv /tmp/argocd /usr/local/bin/argocd
kubectl -n "$ARGO_NS" port-forward svc/argocd-server 8080:443 >/tmp/argocd-pf.log 2>&1 &
PF_PID=$!
sleep 3
ARGO_PASS=$(kubectl -n "$ARGO_NS" get secret argocd-initial-admin-secret -o jsonpath='{.data.password}' | base64 -d)
argocd login localhost:8080 --username admin --password "$ARGO_PASS" --insecure --grpc-web
export ARGOCD_AUTH_TOKEN
ARGOCD_AUTH_TOKEN=$(argocd account generate-token --account admin)
export ARGOCD_SERVER=localhost:8080
export ARGOCD_INSECURE=true
export ARGOCD_BINARY=argocd

echo "==> native CLIFetcher path: argo-impact --app demo-benign --live"
NATIVE_OUT="$OUT_ROOT/native-cli"
mkdir -p "$NATIVE_OUT"
# Live baseline from the kind cluster; desired from argocd app manifests (CLIFetcher).
set +e
"$BIN" argo-impact \
  --app demo-benign \
  --app-namespace "$ARGO_NS" \
  --live \
  --cluster kind \
  --out "$NATIVE_OUT" \
  --quiet | tee "$NATIVE_OUT/cli.txt"
native_code=$?
set -e
echo "scenario=native-cli exit=$native_code" | tee -a "$OUT_ROOT/results.txt"
test -f "$NATIVE_OUT/argo-impact.md"
test -f "$NATIVE_OUT/argo-impact.json"
grep -q "Gate verdict" "$NATIVE_OUT/argo-impact.md"
# Benign sync against a sparse live graph should not BLOCK; allow PASS/WARN/UNKNOWN.
test "$native_code" -eq 0 -o "$native_code" -eq 1 -o "$native_code" -eq 4
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
  for name in native-cli unschedulable netpol-cut pdb-violation unknown-pdb-inventory benign; do
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
