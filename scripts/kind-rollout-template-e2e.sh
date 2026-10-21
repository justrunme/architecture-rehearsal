#!/usr/bin/env bash
# Kind e2e: pdb-rollout template compare via --live (client-side apply dry-run).
# Asserts:
#   - identical desired vs live → WARN/PASS (not BLOCK)
#   - env add → BLOCK
#   - env remove → BLOCK (three-way merge must see removals)
#   - Recreate strategy switch + template annotation → BLOCK via client-side-dry-run (not fail-closed)
#   - shipped PreSync RBAC (with patch) allows the dry-run gate
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"
CLUSTER="${KIND_CLUSTER:-rollout-template-e2e}"
OUT_ROOT="${OUT_ROOT:-$ROOT/out/rollout-template-e2e}"
BIN="${BIN:-$ROOT/bin/rehearsal}"
SAME_DIR="$ROOT/examples/e2e-argo/rollout-template-live"
ENV_DIR="$ROOT/examples/e2e-argo/rollout-template-live-env"
ENV_REMOVED_DIR="$ROOT/examples/e2e-argo/rollout-template-live-env-removed"
RECREATE_DIR="$ROOT/examples/e2e-argo/rollout-template-live-recreate"
RBAC_FILE="$ROOT/deploy/argo-presync/rbac.yaml"

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

compare_method() {
  local out="$1"
  python3 - "$out" <<'PY'
import json, sys
d=json.load(open(sys.argv[1]))
# change envelope is written beside the report
ch_path=sys.argv[1].replace("argo-impact.json","change.json")
try:
    ch=json.load(open(ch_path))
except FileNotFoundError:
    print("MISSING")
    sys.exit(1)
for n in ch.get("patchNodes", []) + ch.get("addedNodes", []):
    attrs=n.get("attributes") or {}
    m=attrs.get("podTemplateCompare")
    if m:
        print(m)
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

echo "==> argo-impact --live with ENV add (expect pdb-rollout BLOCK)"
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

echo "==> apply env version to cluster, then desired WITHOUT env (expect BLOCK on removal)"
kubectl apply -f "$ENV_DIR/app.yaml"
kubectl -n shop rollout status deploy/api --timeout=180s
# Prove last-applied is present (client-side three-way prerequisite).
kubectl -n shop get deploy api -o jsonpath='{.metadata.annotations.kubectl\.kubernetes\.io/last-applied-configuration}' | grep -q FEATURE_FLAGS

ENV_RM_OUT="$OUT_ROOT/env-removed"
mkdir -p "$ENV_RM_OUT"
set +e
"$BIN" argo-impact --live --desired-dir "$ENV_REMOVED_DIR" --namespace shop --app shop \
  --out "$ENV_RM_OUT" --quiet | tee "$ENV_RM_OUT/cli.txt"
code=$?
set -e
st=$(pdb_status "$ENV_RM_OUT/argo-impact.json")
meth=$(compare_method "$ENV_RM_OUT/argo-impact.json" || true)
echo "env-removed pdb-rollout status=$st method=$meth exit=$code"
test "$st" = "BLOCK"
test "$code" -eq 3
case "$meth" in
  client-side-dry-run) ;;
  *)
    echo "FAIL: env removal must use client-side-dry-run (got $meth)"
    exit 1
    ;;
esac

echo "==> restore baseline, then Recreate + annotation (expect BLOCK via client-side-dry-run, not fail-closed)"
kubectl apply -f "$SAME_DIR/app.yaml"
kubectl -n shop rollout status deploy/api --timeout=180s

RECREATE_OUT="$OUT_ROOT/recreate"
mkdir -p "$RECREATE_OUT"
set +e
"$BIN" argo-impact --live --desired-dir "$RECREATE_DIR" --namespace shop --app shop \
  --out "$RECREATE_OUT" --quiet | tee "$RECREATE_OUT/cli.txt"
code=$?
set -e
st=$(pdb_status "$RECREATE_OUT/argo-impact.json")
meth=$(compare_method "$RECREATE_OUT/argo-impact.json" || true)
echo "recreate pdb-rollout status=$st method=$meth exit=$code"
test "$st" = "BLOCK"
test "$code" -eq 3
case "$meth" in
  fail-closed)
    echo "FAIL: Recreate switch must not fail-closed on dry-run (stale rollingUpdate); got method=$meth"
    cat "$RECREATE_OUT/change.json" || true
    exit 1
    ;;
  client-side-dry-run) ;;
  *)
    echo "FAIL: expected client-side-dry-run for Recreate (got $meth)"
    exit 1
    ;;
esac

echo "==> shipped PreSync RBAC: SA with patch can run dry-run gate (identical → WARN/PASS)"
kubectl apply -f "$SAME_DIR/app.yaml"
kubectl -n shop rollout status deploy/api --timeout=180s
# Bind shipped ClusterRole to a dedicated SA in shop (simulate PreSync Job identity).
kubectl -n shop create serviceaccount rehearsal-argo-presync --dry-run=client -o yaml | kubectl apply -f -
# Apply ClusterRole from shipped rbac, then bind subject to shop.
# Apply shipped rbac.yaml (ClusterRole includes patch on workload kinds).
kubectl apply -f "$RBAC_FILE"
grep -A3 'resources: \["deployments"' "$RBAC_FILE" | grep -q patch
kubectl delete clusterrolebinding rehearsal-argo-presync-e2e 2>/dev/null || true
# Bind shipped ClusterRole to the shop PreSync SA (rbac.yaml binds default NS).
kubectl apply -f - <<EOF
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: rehearsal-argo-presync-e2e
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: rehearsal-argo-presync
subjects:
  - kind: ServiceAccount
    name: rehearsal-argo-presync
    namespace: shop
EOF
kubectl auth can-i patch deployments -n shop --as=system:serviceaccount:shop:rehearsal-argo-presync | grep -q yes

RBAC_OUT="$OUT_ROOT/rbac-identical"
mkdir -p "$RBAC_OUT"
# Run under the PreSync SA token via impersonation is not enough for kubectl apply;
# use a kubeconfig with the SA token.
SA_SECRET=$(kubectl -n shop get sa rehearsal-argo-presync -o jsonpath='{.secrets[0].name}' 2>/dev/null || true)
if [[ -z "$SA_SECRET" ]]; then
  # K8s 1.24+: create a token manually
  TOKEN=$(kubectl -n shop create token rehearsal-argo-presync --duration=1h)
else
  TOKEN=$(kubectl -n shop get secret "$SA_SECRET" -o jsonpath='{.data.token}' | base64 -d)
fi
SERVER=$(kubectl config view --minify -o jsonpath='{.clusters[0].cluster.server}')
CA=$(kubectl config view --minify --raw -o jsonpath='{.clusters[0].cluster.certificate-authority-data}')
KC="$RBAC_OUT/sa.kubeconfig"
cat > "$KC" <<EOF
apiVersion: v1
kind: Config
clusters:
- cluster:
    certificate-authority-data: ${CA}
    server: ${SERVER}
  name: kind
contexts:
- context:
    cluster: kind
    user: presync
  name: presync
current-context: presync
users:
- name: presync
  user:
    token: ${TOKEN}
EOF
set +e
"$BIN" argo-impact --live --kubeconfig "$KC" --desired-dir "$SAME_DIR" --namespace shop --app shop \
  --out "$RBAC_OUT" --quiet | tee "$RBAC_OUT/cli.txt"
code=$?
set -e
st=$(pdb_status "$RBAC_OUT/argo-impact.json")
meth=$(compare_method "$RBAC_OUT/argo-impact.json" || true)
echo "rbac-identical pdb-rollout status=$st method=$meth exit=$code"
case "$st" in
  WARN|PASS) ;;
  *)
    echo "FAIL: shipped RBAC must allow dry-run; identical must not BLOCK (got $st method=$meth)"
    cat "$RBAC_OUT/cli.txt" || true
    cat "$RBAC_OUT/change.json" || true
    exit 1
    ;;
esac
case "$meth" in
  fail-closed)
    echo "FAIL: shipped RBAC must not fail-closed on patch denial"
    exit 1
    ;;
esac

echo "OK kind-rollout-template-e2e"
echo "identical=$SAME_OUT env=$ENV_OUT env-removed=$ENV_RM_OUT recreate=$RECREATE_OUT rbac=$RBAC_OUT" | tee "$OUT_ROOT/results.txt"
