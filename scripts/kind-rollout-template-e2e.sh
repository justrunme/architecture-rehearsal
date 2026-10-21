#!/usr/bin/env bash
# Kind e2e: pdb-rollout template compare via --live (client-side apply dry-run).
# Asserts:
#   - identical desired vs live → WARN/PASS (not BLOCK)
#   - env add → BLOCK
#   - env remove → BLOCK (three-way merge must see removals)
#   - Recreate + env → BLOCK by Recreate availability detection (client-side-dry-run)
#   - shipped Role + Nodes/Pods ClusterRole allows the dry-run gate
#   - SSA object (argocd-controller managedFields), no flag, identical → WARN (auto-detect)
#   - cross-namespace pod pressure BLOCKs scale under --namespace/--live-namespace
#   - two-namespace installs keep distinct ClusterRoleBindings / node+pod access
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
RBAC_FILE="$ROOT/deploy/argo-presync/base/rbac.yaml"

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

echo "==> restore baseline, then Recreate + env (expect BLOCK by Recreate availability detection)"
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
    echo "FAIL: Recreate+env must not fail-closed on dry-run; got method=$meth"
    cat "$RECREATE_OUT/change.json" || true
    exit 1
    ;;
  client-side-dry-run) ;;
  *)
    echo "FAIL: expected client-side-dry-run for Recreate+env (got $meth)"
    exit 1
    ;;
esac
grep -q 'Recreate strategy terminates all pods' "$RECREATE_OUT/argo-impact.md" \
  || grep -q 'Recreate strategy terminates all pods' "$RECREATE_OUT/argo-impact.json"

echo "==> shipped PreSync Role + Nodes/Pods ClusterRole via README overlay (identical → WARN/PASS)"
RBAC_NODES_FILE="$ROOT/deploy/argo-presync/base/rbac-nodes.yaml"
EXAMPLE_OVERLAY="$ROOT/deploy/argo-presync/overlays/example"
kubectl apply -f "$SAME_DIR/app.yaml"
kubectl -n shop rollout status deploy/api --timeout=180s
grep -q 'kind: Role' "$RBAC_FILE"
grep -q 'nodes' "$RBAC_NODES_FILE"
grep -A2 'resources: \["pods"\]' "$RBAC_NODES_FILE" | grep -q 'verbs: \["list"\]'
grep -A3 'resources: \["deployments"' "$RBAC_FILE" | grep -q patch
# Install gate RBAC exactly as README overlay pattern (namespace + replacements).
# Write under deploy/argo-presync/overlays so resources: ../../ resolves like the example.
SHOP_OVERLAY="$ROOT/deploy/argo-presync/overlays/shop-e2e"
mkdir -p "$SHOP_OVERLAY"
cp "$EXAMPLE_OVERLAY/kustomization.yaml" "$SHOP_OVERLAY/kustomization.yaml"
sed -i 's/namespace: default/namespace: shop/' "$SHOP_OVERLAY/kustomization.yaml"
kubectl apply -k "$SHOP_OVERLAY"
rm -rf "$SHOP_OVERLAY"
# RoleBinding subject is set by overlay namespace; Role comes from base.
kubectl auth can-i patch deployments -n shop --as=system:serviceaccount:shop:rehearsal-argo-presync | grep -q yes
kubectl auth can-i get nodes --as=system:serviceaccount:shop:rehearsal-argo-presync | grep -q yes
kubectl auth can-i list pods --as=system:serviceaccount:shop:rehearsal-argo-presync | grep -q yes
# get on pods must NOT be granted (list only)
if kubectl auth can-i get pods --as=system:serviceaccount:shop:rehearsal-argo-presync 2>/dev/null | grep -q yes; then
  echo "FAIL: pods get must not be granted (list only)"
  exit 1
fi
test "$(kubectl get clusterrolebinding rehearsal-argo-presync-nodes-in-shop -o jsonpath='{.roleRef.name}')" = "rehearsal-argo-presync-nodes-in-shop"

RBAC_OUT="$OUT_ROOT/rbac-identical"
mkdir -p "$RBAC_OUT"
TOKEN=$(kubectl -n shop create token rehearsal-argo-presync --duration=1h)
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
"$BIN" argo-impact --live --kubeconfig "$KC" --live-namespace shop \
  --desired-dir "$SAME_DIR" --namespace shop --app shop \
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

echo "==> SSA auto-detect: apply with argocd-controller, identical desired, no --server-side-apply → WARN"
kubectl delete deploy -n shop api --wait=true --timeout=60s 2>/dev/null || true
# Strip last-applied by using server-side apply with Argo's manager.
kubectl apply --server-side --force-conflicts --field-manager=argocd-controller -f "$SAME_DIR/app.yaml"
kubectl -n shop rollout status deploy/api --timeout=180s
# Confirm managedFields visible only with --show-managed-fields (gate relies on this).
kubectl -n shop get deploy api -o json --show-managed-fields | grep -q 'argocd-controller'

SSA_OUT="$OUT_ROOT/ssa-identical"
mkdir -p "$SSA_OUT"
set +e
"$BIN" argo-impact --live --desired-dir "$SAME_DIR" --namespace shop --app shop \
  --out "$SSA_OUT" --quiet | tee "$SSA_OUT/cli.txt"
code=$?
set -e
st=$(pdb_status "$SSA_OUT/argo-impact.json")
meth=$(compare_method "$SSA_OUT/argo-impact.json" || true)
echo "ssa-identical pdb-rollout status=$st method=$meth exit=$code"
case "$st" in
  WARN|PASS) ;;
  *)
    echo "FAIL: SSA auto-detect identical must WARN/PASS not BLOCK/UNKNOWN (got $st method=$meth)"
    cat "$SSA_OUT/change.json" || true
    cat "$SSA_OUT/argo-impact.md" || true
    exit 1
    ;;
esac
case "$meth" in
  fail-closed)
    echo "FAIL: SSA auto-detect must not fail-closed (no-last-applied); got method=$meth"
    cat "$SSA_OUT/change.json" || true
    exit 1
    ;;
esac
# When dry-run compare attrs are present, they must show SSA (not client fail-closed).
if [[ "$meth" != "MISSING" && -n "$meth" ]]; then
  test "$meth" = "server-side-dry-run"
fi

echo "==> C: cross-namespace pods fill node; scale under --namespace/--live-namespace → BLOCK"
kubectl apply -f "$SAME_DIR/app.yaml"
kubectl -n shop rollout status deploy/api --timeout=180s
ALLOC=$(kubectl get node -o jsonpath='{.items[0].status.allocatable.pods}')
# Fill until ≤2 free slots. Live app uses maxSurge 0, so scale 3→6 needs 3 slots → BLOCK.
# (VERIFY-7 used default 25% surge needing 5; either way namespaced inventory must not false-PASS.)
FILLER=$((ALLOC - 8))
if [[ "$FILLER" -lt 40 ]]; then FILLER=40; fi
kubectl create namespace other --dry-run=client -o yaml | kubectl apply -f -
# Prefer a local pause image already in kind; fall back to registry.k8s.io.
PAUSE_IMG=registry.k8s.io/pause:3.9
kubectl -n other apply -f - <<EOF
apiVersion: apps/v1
kind: Deployment
metadata:
  name: filler
spec:
  replicas: ${FILLER}
  selector:
    matchLabels:
      app: filler
  template:
    metadata:
      labels:
        app: filler
    spec:
      terminationGracePeriodSeconds: 0
      containers:
        - name: pause
          image: ${PAUSE_IMG}
          resources:
            requests:
              cpu: 1m
              memory: 8Mi
EOF
for i in $(seq 1 90); do
  running=$(kubectl get pods -A --field-selector=status.phase!=Succeeded,status.phase!=Failed --no-headers 2>/dev/null | wc -l | tr -d ' ')
  free=$((ALLOC - running))
  echo "capacity wait: running=$running alloc=$ALLOC free=$free"
  if [[ "$free" -le 2 ]]; then
    break
  fi
  # Top up filler if the scheduler is still accepting pods.
  if [[ "$i" -eq 30 || "$i" -eq 60 ]]; then
    cur=$(kubectl -n other get deploy filler -o jsonpath='{.spec.replicas}')
    kubectl -n other scale deploy/filler --replicas=$((cur + 20)) || true
  fi
  sleep 2
done
running=$(kubectl get pods -A --field-selector=status.phase!=Succeeded,status.phase!=Failed --no-headers 2>/dev/null | wc -l | tr -d ' ')
free=$((ALLOC - running))
if [[ "$free" -gt 2 ]]; then
  echo "FAIL: could not fill node enough for capacity repro (free=$free alloc=$ALLOC)"
  kubectl get pods -A | head -50 || true
  exit 1
fi
SCALE_DIR="$OUT_ROOT/v7-xns-scale"
mkdir -p "$SCALE_DIR"
python3 - "$SAME_DIR/app.yaml" "$SCALE_DIR/app.yaml" <<'PY'
import sys, re
src, dst = sys.argv[1], sys.argv[2]
text = open(src).read()
# Bump Deployment replicas 3→6; keep maxSurge 0 from live (delta=3 slots needed).
text2, n = re.subn(r"(replicas:\s*)3\b", r"\g<1>6", text, count=1)
if n != 1:
    raise SystemExit(f"expected to rewrite one replicas: 3, got {n}")
open(dst, "w").write(text2)
PY
XNS_OUT="$OUT_ROOT/xns-scale"
mkdir -p "$XNS_OUT"
set +e
"$BIN" argo-impact --live --desired-dir "$SCALE_DIR" --namespace shop --live-namespace shop --app shop \
  --out "$XNS_OUT" --quiet | tee "$XNS_OUT/cli.txt"
xns_code=$?
set -e
sched_status=$(python3 - "$XNS_OUT/argo-impact.json" <<'PY'
import json, sys
d=json.load(open(sys.argv[1]))
for det in d.get("detectors", []):
    if det.get("detector") == "scheduling-capacity":
        print(det.get("status", ""))
        sys.exit(0)
print("MISSING")
PY
)
echo "xns-scale scheduling-capacity=$sched_status exit=$xns_code free=$free"
if [[ "$sched_status" != "BLOCK" ]]; then
  echo "FAIL: cross-ns full node + scale under --namespace must BLOCK scheduling-capacity (got $sched_status)"
  cat "$XNS_OUT/argo-impact.md" || true
  cat "$XNS_OUT/argo-impact.json" || true
  exit 1
fi
kubectl -n other delete deploy filler --wait=false 2>/dev/null || true

echo "==> F: CPU hog in other ns; scale under --namespace → BLOCK (not false PASS)"
kubectl apply -f "$SAME_DIR/app.yaml"
kubectl -n shop rollout status deploy/api --timeout=180s
# Raise live requests to 150m (VERIFY-8 repro) so new pods demand meaningful CPU.
kubectl -n shop patch deploy api --type=json -p='[
  {"op":"replace","path":"/spec/template/spec/containers/0/resources/requests/cpu","value":"150m"}
]'
kubectl -n shop rollout status deploy/api --timeout=180s
ALLOC_CPU_RAW=$(kubectl get node -o jsonpath='{.items[0].status.allocatable.cpu}')
# Convert cores or millicores to millicores.
if [[ "$ALLOC_CPU_RAW" == *m ]]; then
  ALLOC_CPU_M=${ALLOC_CPU_RAW%m}
else
  ALLOC_CPU_M=$(python3 -c "print(int(float('$ALLOC_CPU_RAW')*1000))")
fi
# Leave ~100m free after hog so 5×150m (delta+surge) cannot fit; pods without
# requests (kube-proxy etc.) must not disable the CPU check.
HOG_CPU=$((ALLOC_CPU_M - 100))
if [[ "$HOG_CPU" -lt 500 ]]; then HOG_CPU=500; fi
kubectl create namespace other --dry-run=client -o yaml | kubectl apply -f -
kubectl -n other apply -f - <<EOF
apiVersion: apps/v1
kind: Deployment
metadata:
  name: hog
spec:
  replicas: 1
  selector:
    matchLabels:
      app: hog
  template:
    metadata:
      labels:
        app: hog
    spec:
      terminationGracePeriodSeconds: 0
      containers:
        - name: pause
          image: registry.k8s.io/pause:3.9
          resources:
            requests:
              cpu: ${HOG_CPU}m
              memory: 8Mi
EOF
kubectl -n other rollout status deploy/hog --timeout=120s || true
CPU_DIR="$OUT_ROOT/v8-cpuhog-scale"
mkdir -p "$CPU_DIR"
python3 - "$SAME_DIR/app.yaml" "$CPU_DIR/app.yaml" <<'PY'
import sys, re
src, dst = sys.argv[1], sys.argv[2]
text = open(src).read()
text = re.sub(r"(replicas:\s*)3\b", r"\g<1>6", text, count=1)
text = re.sub(r"(cpu:\s*)50m\b", r"\g<1>150m", text, count=1)
open(dst, "w").write(text)
PY
CPU_OUT="$OUT_ROOT/cpuhog-scale"
mkdir -p "$CPU_OUT"
set +e
"$BIN" argo-impact --live --desired-dir "$CPU_DIR" --namespace shop --live-namespace shop --app shop \
  --out "$CPU_OUT" --quiet | tee "$CPU_OUT/cli.txt"
cpu_code=$?
set -e
cpu_sched=$(python3 - "$CPU_OUT/argo-impact.json" <<'PY'
import json, sys
d=json.load(open(sys.argv[1]))
for det in d.get("detectors", []):
    if det.get("detector") == "scheduling-capacity":
        print(det.get("status", ""))
        sys.exit(0)
print("MISSING")
PY
)
echo "cpuhog-scale scheduling-capacity=$cpu_sched exit=$cpu_code hog=${HOG_CPU}m alloc=${ALLOC_CPU_M}m"
if [[ "$cpu_sched" != "BLOCK" ]]; then
  echo "FAIL: CPU hog + scale must BLOCK scheduling-capacity (got $cpu_sched)"
  cat "$CPU_OUT/argo-impact.md" || true
  cat "$CPU_OUT/argo-impact.json" || true
  exit 1
fi
kubectl -n other delete deploy hog --wait=false 2>/dev/null || true
kubectl -n other delete deploy filler --wait=false 2>/dev/null || true

echo "==> E': two README-style overlays keep distinct per-ns ClusterRole + Binding"
kubectl create namespace shop2 --dry-run=client -o yaml | kubectl apply -f -
EXAMPLE_OVERLAY="$ROOT/deploy/argo-presync/overlays/example"
for ns in shop shop2; do
  # Same layout as overlays/example so ../../ points at the base.
  ov="$ROOT/deploy/argo-presync/overlays/${ns}-e2e"
  mkdir -p "$ov"
  cp "$EXAMPLE_OVERLAY/kustomization.yaml" "$ov/kustomization.yaml"
  sed -i "s/namespace: default/namespace: ${ns}/" "$ov/kustomization.yaml"
  # Prove build renames before apply (VERIFY-8 failure mode was …-in-default).
  built=$(kubectl kustomize "$ov")
  echo "$built" | grep -q "name: rehearsal-argo-presync-nodes-in-${ns}"
  echo "$built" | grep -q "kind: ClusterRole"
  echo "$built" | grep -q "kind: ClusterRoleBinding"
  if echo "$built" | grep -q 'name: rehearsal-argo-presync-nodes-in-default'; then
    echo "FAIL: overlay $ns still emits …-in-default"
    rm -rf "$ov"
    exit 1
  fi
  kubectl apply -k "$ov"
  rm -rf "$ov"
done
shop_subj=$(kubectl get clusterrolebinding rehearsal-argo-presync-nodes-in-shop -o jsonpath='{.subjects[0].namespace}')
shop2_subj=$(kubectl get clusterrolebinding rehearsal-argo-presync-nodes-in-shop2 -o jsonpath='{.subjects[0].namespace}')
shop_role=$(kubectl get clusterrolebinding rehearsal-argo-presync-nodes-in-shop -o jsonpath='{.roleRef.name}')
shop2_role=$(kubectl get clusterrolebinding rehearsal-argo-presync-nodes-in-shop2 -o jsonpath='{.roleRef.name}')
echo "CRB: shop subj=$shop_subj role=$shop_role | shop2 subj=$shop2_subj role=$shop2_role"
test "$shop_subj" = "shop"
test "$shop2_subj" = "shop2"
test "$shop_role" = "rehearsal-argo-presync-nodes-in-shop"
test "$shop2_role" = "rehearsal-argo-presync-nodes-in-shop2"
kubectl get clusterrole rehearsal-argo-presync-nodes-in-shop >/dev/null
kubectl get clusterrole rehearsal-argo-presync-nodes-in-shop2 >/dev/null
kubectl auth can-i get nodes --as=system:serviceaccount:shop:rehearsal-argo-presync | grep -q yes
kubectl auth can-i get nodes --as=system:serviceaccount:shop2:rehearsal-argo-presync | grep -q yes
kubectl auth can-i list pods --as=system:serviceaccount:shop:rehearsal-argo-presync | grep -q yes
kubectl auth can-i list pods --as=system:serviceaccount:shop2:rehearsal-argo-presync | grep -q yes

echo "OK kind-rollout-template-e2e"
echo "identical=$SAME_OUT env=$ENV_OUT env-removed=$ENV_RM_OUT recreate=$RECREATE_OUT rbac=$RBAC_OUT ssa=$SSA_OUT xns=$XNS_OUT cpu=$CPU_OUT" | tee "$OUT_ROOT/results.txt"
