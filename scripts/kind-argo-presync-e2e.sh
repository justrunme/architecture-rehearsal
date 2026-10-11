#!/usr/bin/env bash
# Kind + Argo CD PreSync change-impact gate E2E.
# Proves: (a) safe sync Healthy (b) risky blocked, live object unchanged
#          (c) fail-closed on gate failure (d) break-glass override logged.
# Asserts concrete Argo status fields — never masks failures with || true on checks.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"
CLUSTER="${KIND_CLUSTER:-argo-presync-e2e}"
ARGO_NS="${ARGO_NS:-argocd}"
ARGO_VERSION="${ARGO_VERSION:-v2.14.9}"
OUT_ROOT="${OUT_ROOT:-$ROOT/out/argo-presync-e2e}"
IMAGE="${PRESYNC_IMAGE:-architecture-rehearsal-presync:e2e}"
BIN="${BIN:-$ROOT/bin/rehearsal}"

need() { command -v "$1" >/dev/null || { echo "missing required tool: $1"; exit 1; }; }
need kind
need kubectl
need docker
need git
need python3

cleanup() {
  if [[ -n "${GIT_DAEMON_PID:-}" ]]; then kill "$GIT_DAEMON_PID" 2>/dev/null || true; fi
  if [[ "${KEEP_CLUSTER:-}" != "1" ]]; then kind delete cluster --name "$CLUSTER" 2>/dev/null || true; fi
}
trap cleanup EXIT

mkdir -p "$OUT_ROOT"
: > "$OUT_ROOT/results.txt"
: > "$OUT_ROOT/metrics.txt"

echo "==> unit-level smoke: build + go test presync"
go test ./internal/presync/ -count=1
go build -o "$BIN" ./cmd/rehearsal

# Import host-pulled images into kind (needed when the node cannot pull quay.io / registry.k8s.io).
kind_import_image() {
  local img="$1"
  local tag safe
  tag=$(echo "$img" | tr '/:' '__')
  safe="/${tag}.tar"
  echo "  import $img"
  docker pull --platform=linux/amd64 "$img" >/dev/null
  # Flatten attestations/multi-arch indexes that break containerd import+resolve.
  echo "FROM $img" | docker build --provenance=false --sbom=false -t "$img" - >/dev/null
  docker save -o "/tmp/${tag}.tar" "$img"
  docker cp "/tmp/${tag}.tar" "${CLUSTER}-control-plane:${safe}"
  docker exec "${CLUSTER}-control-plane" ctr -n k8s.io images import "$safe" >/dev/null
  docker exec "${CLUSTER}-control-plane" rm -f "$safe"
  rm -f "/tmp/${tag}.tar"
}

patch_argocd_never_pull() {
  local kind=$1 name=$2
  kubectl -n "$ARGO_NS" get "$kind" "$name" -o json | python3 -c '
import json,sys
o=json.load(sys.stdin)
spec=o["spec"]["template"]["spec"]
for c in spec.get("containers",[])+spec.get("initContainers",[]):
  c["imagePullPolicy"]="Never"
for k in ("status","managedFields"):
  o.pop(k, None)
md=o["metadata"]
for k in ("resourceVersion","uid","generation","creationTimestamp","managedFields"):
  md.pop(k, None)
json.dump(o, sys.stdout)
' | kubectl -n "$ARGO_NS" apply -f -
}

if [[ "${SKIP_KIND_SETUP:-}" != "1" ]]; then
  echo "==> kind create $CLUSTER"
  if [[ "${KEEP_CLUSTER:-}" != "1" ]]; then kind delete cluster --name "$CLUSTER" 2>/dev/null || true; fi
  kind create cluster --name "$CLUSTER"

  echo "==> build PreSync image $IMAGE"
  docker build --provenance=false --sbom=false -t "$IMAGE" -f Dockerfile.presync .

  # Always load the gate image + pause workload into kind.
  kind load docker-image "$IMAGE" --name "$CLUSTER"
  kind_import_image "registry.k8s.io/pause:3.9"

  # On GHA, Argo images pull from quay.io. Locally, preload when PRESYNC_PRELOAD_ARGO=1 (default off on GHA).
  if [[ "${PRESYNC_PRELOAD_ARGO:-}" == "1" ]] || [[ -z "${GITHUB_ACTIONS:-}" && "${PRESYNC_PRELOAD_ARGO:-1}" == "1" ]]; then
    echo "==> preload Argo CD images into kind"
    kind_import_image "quay.io/argoproj/argocd:${ARGO_VERSION}"
    kind_import_image "ghcr.io/dexidp/dex:v2.41.1"
    kind_import_image "redis:7.0.15-alpine"
  fi

  echo "==> install Argo CD $ARGO_VERSION"
  kubectl create namespace "$ARGO_NS"
  kubectl apply -n "$ARGO_NS" -f "https://raw.githubusercontent.com/argoproj/argo-cd/${ARGO_VERSION}/manifests/install.yaml"
  if [[ -z "${GITHUB_ACTIONS:-}" ]] || [[ "${PRESYNC_PRELOAD_ARGO:-}" == "1" ]]; then
    for d in argocd-server argocd-repo-server argocd-redis argocd-dex-server \
             argocd-notifications-controller argocd-applicationset-controller; do
      patch_argocd_never_pull deploy "$d" || true
    done
    patch_argocd_never_pull sts argocd-application-controller || true
    kubectl -n "$ARGO_NS" delete pods --all --force --grace-period=0 2>/dev/null || true
  fi
  kubectl -n "$ARGO_NS" wait --for=condition=Available deploy/argocd-server --timeout=300s
  kubectl -n "$ARGO_NS" wait --for=condition=Available deploy/argocd-repo-server --timeout=300s
  kubectl -n "$ARGO_NS" rollout status statefulset/argocd-application-controller --timeout=300s
else
  echo "==> SKIP_KIND_SETUP=1 — reusing existing kind cluster $CLUSTER"
  kubectl config use-context "kind-${CLUSTER}"
  echo "==> rebuild + reload PreSync image $IMAGE"
  docker build --provenance=false --sbom=false -t "$IMAGE" -f Dockerfile.presync .
  kind load docker-image "$IMAGE" --name "$CLUSTER"
fi

# ---------------------------------------------------------------------------
# Application source: GitHub on CI (and when ARGO_E2E_REPO_URL is set);
# otherwise an in-cluster git-daemon serving a bare repo of the fixtures.
# ---------------------------------------------------------------------------
REPO_REV="${ARGO_E2E_REPO_REVISION:-${GITHUB_SHA:-$(git rev-parse HEAD)}}"
if [[ -n "${ARGO_E2E_REPO_URL:-}" ]] || [[ -n "${GITHUB_ACTIONS:-}" ]]; then
  REPO_URL="${ARGO_E2E_REPO_URL:-https://github.com/justrunme/architecture-rehearsal.git}"
  echo "==> Application source: GitHub $REPO_URL @$REPO_REV"
else
  echo "==> local git bare repo + in-cluster git-daemon"
  GIT_PARENT="$OUT_ROOT/git-parent"
  rm -rf "$GIT_PARENT"
  mkdir -p "$GIT_PARENT/presync-src/examples"
  cp -a examples/e2e-argo-presync "$GIT_PARENT/presync-src/examples/"
  (
    cd "$GIT_PARENT/presync-src"
    git init -b main
    git config user.email "e2e@rehearsal.local"
    git config user.name "e2e"
    # Tolerate "dubious ownership" inside containers that mount this tree.
    git config --global --add safe.directory '*'
    git add examples
    git commit -m "presync e2e fixtures"
  )
  git clone --bare "$GIT_PARENT/presync-src" "$GIT_PARENT/presync.git"
  git -C "$GIT_PARENT/presync.git" update-server-info
  REPO_REV=$(git -C "$GIT_PARENT/presync-src" rev-parse HEAD)
  git --git-dir="$GIT_PARENT/presync.git" ls-tree -d --name-only HEAD:examples/e2e-argo-presync | tee "$OUT_ROOT/git-paths.txt"
  grep -q '^safe$' "$OUT_ROOT/git-paths.txt"

  # Debian git includes `git daemon` (alpine/git does not).
  docker build --provenance=false --sbom=false -t git-daemon:e2e - <<'EOF'
FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends git ca-certificates \
  && rm -rf /var/lib/apt/lists/* \
  && git config --global --add safe.directory '*'
EOF
  kind load docker-image git-daemon:e2e --name "$CLUSTER"
  docker exec "${CLUSTER}-control-plane" rm -rf /presync-git
  docker cp "$GIT_PARENT/presync.git" "${CLUSTER}-control-plane:/presync-git"
  docker exec "${CLUSTER}-control-plane" chmod -R a+rX /presync-git
  kubectl delete ns git-server --ignore-not-found >/dev/null 2>&1 || true
  kubectl create namespace git-server
  cat <<'EOF' | kubectl apply -f -
apiVersion: v1
kind: Pod
metadata:
  name: git-daemon
  namespace: git-server
  labels:
    app: git-daemon
spec:
  containers:
    - name: git-daemon
      image: git-daemon:e2e
      imagePullPolicy: Never
      ports:
        - containerPort: 9418
      command: ["/bin/sh","-c"]
      args:
        - |
          set -e
          git config --global --add safe.directory '*'
          exec git daemon --reuseaddr --export-all --base-path=/git --verbose --listen=0.0.0.0 --port=9418
      volumeMounts:
        - name: git
          mountPath: /git/presync
          readOnly: true
  volumes:
    - name: git
      hostPath:
        path: /presync-git
        type: Directory
---
apiVersion: v1
kind: Service
metadata:
  name: git-daemon
  namespace: git-server
spec:
  selector:
    app: git-daemon
  ports:
    - port: 9418
      targetPort: 9418
EOF
  kubectl -n git-server wait --for=condition=Ready pod/git-daemon --timeout=120s
  # Some kind/CNI setups refuse ClusterIP from pods; pod IP is reachable on the node network.
  GIT_IP=$(kubectl -n git-server get pod git-daemon -o jsonpath='{.status.podIP}')
  test -n "$GIT_IP"
  REPO_URL="git://${GIT_IP}/presync"
  echo "  REPO_URL=$REPO_URL rev=$REPO_REV"
  # Smoke: clone from repo-server
  kubectl -n "$ARGO_NS" exec deploy/argocd-repo-server -c argocd-repo-server -- \
    sh -c "rm -rf /tmp/presync-smoke && git clone --depth 1 '${REPO_URL}' /tmp/presync-smoke && test -f /tmp/presync-smoke/examples/e2e-argo-presync/safe/deployment.yaml" \
    | tee "$OUT_ROOT/git-ls-remote.txt"

  kubectl -n "$ARGO_NS" apply -f - <<EOF
apiVersion: v1
kind: Secret
metadata:
  name: presync-e2e-repo
  namespace: ${ARGO_NS}
  labels:
    argocd.argoproj.io/secret-type: repository
stringData:
  type: git
  url: ${REPO_URL}
  insecure: "true"
EOF
fi

kubectl -n "$ARGO_NS" patch configmap argocd-cm --type merge -p '{"data":{"timeout.reconciliation":"15s"}}'

wait_jsonpath() {
  # wait_jsonpath <ns> <resource> <jsonpath> <want> <timeout_sec> <desc>
  local ns=$1 res=$2 jp=$3 want=$4 timeout=$5 desc=$6
  local i max=$((timeout / 2))
  for i in $(seq 1 "$max"); do
    got=$(kubectl -n "$ns" get "$res" -o "jsonpath=${jp}" 2>/dev/null || true)
    echo "  [$i] $desc: got=${got:-<empty>} want=$want"
    if [[ "$got" == "$want" ]]; then
      return 0
    fi
    sleep 2
  done
  echo "FAIL: timeout waiting for $desc ($jp=$want)" >&2
  kubectl -n "$ns" get "$res" -o yaml | tee "$OUT_ROOT/fail-${res//\//-}.yaml" >&2 || true
  return 1
}

wait_app_operation_phase() {
  local app=$1 want=$2 timeout=$3
  local i max=$((timeout / 2))
  for i in $(seq 1 "$max"); do
    phase=$(kubectl -n "$ARGO_NS" get application "$app" -o jsonpath='{.status.operationState.phase}' 2>/dev/null || true)
    msg=$(kubectl -n "$ARGO_NS" get application "$app" -o jsonpath='{.status.operationState.message}' 2>/dev/null || true)
    echo "  [$i] operation.phase=${phase:-<empty>} msg=${msg:0:160}"
    if [[ "$phase" == "$want" ]]; then
      return 0
    fi
    if [[ "$want" == "Failed" && "$phase" == "Error" ]]; then
      return 0
    fi
    sleep 2
  done
  echo "FAIL: timeout waiting for application/$app operation phase=$want" >&2
  kubectl -n "$ARGO_NS" get application "$app" -o yaml | tee "$OUT_ROOT/fail-app-$app.yaml" >&2 || true
  return 1
}

create_app() {
  local name=$1 path=$2 dest_ns=$3
  local breakglass="${4:-}"
  kubectl create namespace "$dest_ns" --dry-run=client -o yaml | kubectl apply -f -
  local annot=""
  if [[ "$breakglass" == "breakglass" ]]; then
    annot=$(cat <<EOF
  annotations:
    rehearsal.io/break-glass: "true"
    rehearsal.io/break-glass-reason: "INC-e2e emergency override"
EOF
)
  fi
  cat <<EOF | kubectl apply -f -
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: ${name}
  namespace: ${ARGO_NS}
${annot}
spec:
  project: default
  source:
    repoURL: ${REPO_URL}
    targetRevision: ${REPO_REV}
    path: ${path}
  destination:
    server: https://kubernetes.default.svc
    namespace: ${dest_ns}
  syncPolicy:
    syncOptions:
      - CreateNamespace=true
EOF
}

sync_app() {
  local name=$1
  kubectl -n "$ARGO_NS" patch application "$name" --type merge -p \
    "{\"operation\":{\"initiatedBy\":{\"username\":\"e2e\"},\"sync\":{\"revision\":\"${REPO_REV}\"}}}"
}

hook_logs() {
  local dest_ns=$1
  local out=$2
  # Jobs may still be terminating; retry briefly for the newest gate pod.
  local pod="" i
  for i in $(seq 1 30); do
    kubectl -n "$dest_ns" get jobs,pods -l app.kubernetes.io/name=rehearsal-argo-presync -o wide | tee "$out.list" >/dev/null || true
    pod=$(kubectl -n "$dest_ns" get pods -l app.kubernetes.io/name=rehearsal-argo-presync --sort-by=.metadata.creationTimestamp -o jsonpath='{.items[-1:].metadata.name}' 2>/dev/null || true)
    if [[ -n "$pod" ]]; then
      break
    fi
    sleep 1
  done
  cat "$out.list" 2>/dev/null || true
  if [[ -n "$pod" ]]; then
    kubectl -n "$dest_ns" logs "$pod" -c gate 2>/dev/null | tee "$out" \
      || kubectl -n "$dest_ns" logs "$pod" | tee "$out" \
      || echo "no logs for $pod" | tee "$out"
  else
    echo "no gate pod found in $dest_ns" | tee "$out"
  fi
}

extract_decision_line() {
  local logf=$1
  python3 - "$logf" <<'PY'
import json,sys
path=sys.argv[1]
line=None
for raw in open(path,errors="replace"):
    s=raw.strip()
    if s.startswith("{") and '"decision"' in s:
        line=s
if not line:
    sys.exit("no decision JSON line in "+path)
obj=json.loads(line)
print(json.dumps(obj,sort_keys=True))
open(path+".decision.json","w").write(json.dumps(obj,indent=2)+"\n")
print("decision="+obj.get("decision",""))
print("durationMs="+str(obj.get("durationMs","")))
PY
}

METRICS_SAFE_TOTAL=0
METRICS_SAFE_FALSE_BLOCK=0
METRICS_RISKY_TOTAL=0
METRICS_RISKY_BLOCKED=0
METRICS_LATENCIES=()

record_latency() {
  local ms=$1
  if [[ -n "$ms" && "$ms" != "None" ]]; then
    METRICS_LATENCIES+=("$ms")
  fi
}

########################################
# (a) Safe change syncs Healthy
########################################
echo "==> (a) safe PreSync → Synced + Healthy"
# Clean prior app/ns from re-runs
kubectl -n "$ARGO_NS" delete application presync-safe --ignore-not-found >/dev/null 2>&1 || true
kubectl delete ns presync-demo --ignore-not-found >/dev/null 2>&1 || true
# Wait for namespace termination
for i in $(seq 1 60); do
  kubectl get ns presync-demo >/dev/null 2>&1 || break
  sleep 1
done
create_app "presync-safe" "examples/e2e-argo-presync/safe" "presync-demo"
# Wait until Argo has rendered manifests from the repo
for i in $(seq 1 60); do
  if kubectl -n "$ARGO_NS" get application presync-safe -o jsonpath='{.status.sync.status}' 2>/dev/null | grep -Eq 'OutOfSync|Synced'; then
    break
  fi
  sleep 2
done
SYNC_START=$(date +%s%3N)
sync_app "presync-safe"
wait_jsonpath "$ARGO_NS" "application/presync-safe" "{.status.sync.status}" "Synced" 180 "safe sync"
wait_jsonpath "$ARGO_NS" "application/presync-safe" "{.status.health.status}" "Healthy" 180 "safe health"
SYNC_END=$(date +%s%3N)
kubectl -n "$ARGO_NS" get application presync-safe -o yaml | tee "$OUT_ROOT/app-safe.yaml" >/dev/null
kubectl -n presync-demo get deploy demo-api -o yaml | tee "$OUT_ROOT/deploy-safe.yaml" >/dev/null
REPLICAS=$(kubectl -n presync-demo get deploy demo-api -o jsonpath='{.spec.replicas}')
test "$REPLICAS" = "1"
LABEL=$(kubectl -n presync-demo get deploy demo-api -o jsonpath='{.metadata.labels.e2e\.rehearsal\.io/scenario}')
test "$LABEL" = "safe"
hook_logs presync-demo "$OUT_ROOT/logs-safe.txt"
extract_decision_line "$OUT_ROOT/logs-safe.txt" | tee "$OUT_ROOT/safe-decision.txt"
grep -q 'decision=ALLOW\|decision=WARN' "$OUT_ROOT/safe-decision.txt"
SAFE_MS=$(python3 -c 'import json; print(json.load(open("'"$OUT_ROOT"'/logs-safe.txt.decision.json")).get("durationMs",""))')
record_latency "$SAFE_MS"
METRICS_SAFE_TOTAL=$((METRICS_SAFE_TOTAL+1))
echo "scenario=safe result=pass sync_ms=$((SYNC_END-SYNC_START)) gate_ms=$SAFE_MS" | tee -a "$OUT_ROOT/results.txt"

########################################
# (b) Risky change blocked; live object NOT changed
# Use a fresh Application (same live namespace) so prior safe PreSync hooks are not reused.
########################################
echo "==> (b) risky PreSync BLOCK — live Deployment unchanged"
BEFORE_REP=$(kubectl -n presync-demo get deploy demo-api -o jsonpath='{.spec.replicas}')
BEFORE_SEL=$(kubectl -n presync-demo get deploy demo-api -o jsonpath='{.spec.template.spec.nodeSelector.e2e\.rehearsal\.io/no-such-node}')
test "$BEFORE_REP" = "1"
test -z "$BEFORE_SEL"
# Drop the safe Application but keep the live Deployment baseline in-cluster.
kubectl -n "$ARGO_NS" delete application presync-safe --wait=true --timeout=60s
# Remove leftover PreSync hooks from the safe sync (keep Deployment).
kubectl -n presync-demo delete job,sa,cm -l app.kubernetes.io/name=rehearsal-argo-presync --wait=true --timeout=60s 2>/dev/null || true
kubectl -n presync-demo delete cm rehearsal-argo-presync-desired --ignore-not-found >/dev/null 2>&1 || true
kubectl -n presync-demo delete sa rehearsal-argo-presync --ignore-not-found >/dev/null 2>&1 || true
kubectl delete clusterrole,clusterrolebinding -l app.kubernetes.io/name=rehearsal-argo-presync --ignore-not-found >/dev/null 2>&1 || true
# Confirm baseline still present
test "$(kubectl -n presync-demo get deploy demo-api -o jsonpath='{.metadata.labels.e2e\.rehearsal\.io/scenario}')" = "safe"
create_app "presync-risky" "examples/e2e-argo-presync/risky" "presync-demo"
for i in $(seq 1 60); do
  if kubectl -n "$ARGO_NS" get application presync-risky -o jsonpath='{.status.sync.status}' 2>/dev/null | grep -Eq 'OutOfSync|Synced'; then
    break
  fi
  sleep 2
done
sync_app "presync-risky"
wait_app_operation_phase "presync-risky" "Failed" 180
PHASE=$(kubectl -n "$ARGO_NS" get application presync-risky -o jsonpath='{.status.operationState.phase}')
[[ "$PHASE" == "Failed" || "$PHASE" == "Error" ]]
MSG=$(kubectl -n "$ARGO_NS" get application presync-risky -o jsonpath='{.status.operationState.message}')
echo "  blocked message: $MSG" | tee -a "$OUT_ROOT/results.txt"
kubectl -n "$ARGO_NS" get application presync-risky -o yaml | tee "$OUT_ROOT/app-risky-blocked.yaml" >/dev/null
AFTER_REP=$(kubectl -n presync-demo get deploy demo-api -o jsonpath='{.spec.replicas}')
AFTER_SEL=$(kubectl -n presync-demo get deploy demo-api -o jsonpath='{.spec.template.spec.nodeSelector.e2e\.rehearsal\.io/no-such-node}')
AFTER_LABEL=$(kubectl -n presync-demo get deploy demo-api -o jsonpath='{.metadata.labels.e2e\.rehearsal\.io/scenario}')
test "$AFTER_REP" = "1"
test -z "$AFTER_SEL"
test "$AFTER_LABEL" = "safe"
hook_logs presync-demo "$OUT_ROOT/logs-risky.txt"
extract_decision_line "$OUT_ROOT/logs-risky.txt" | tee "$OUT_ROOT/risky-decision.txt"
grep -q 'decision=BLOCK' "$OUT_ROOT/risky-decision.txt"
RISKY_MS=$(python3 -c 'import json; print(json.load(open("'"$OUT_ROOT"'/logs-risky.txt.decision.json")).get("durationMs",""))')
record_latency "$RISKY_MS"
METRICS_RISKY_TOTAL=$((METRICS_RISKY_TOTAL+1))
METRICS_RISKY_BLOCKED=$((METRICS_RISKY_BLOCKED+1))
echo "scenario=risky result=blocked gate_ms=$RISKY_MS live_replicas=$AFTER_REP live_label=$AFTER_LABEL" | tee -a "$OUT_ROOT/results.txt"

########################################
# (c) Fail-closed: gate cannot compute → sync blocked
########################################
echo "==> (c) fail-closed PreSync (missing desired) → operation Failed"
kubectl -n "$ARGO_NS" delete application presync-failclosed --ignore-not-found >/dev/null 2>&1 || true
kubectl delete ns presync-failclosed --ignore-not-found >/dev/null 2>&1 || true
for i in $(seq 1 60); do
  kubectl get ns presync-failclosed >/dev/null 2>&1 || break
  sleep 1
done
create_app "presync-failclosed" "examples/e2e-argo-presync/failclosed" "presync-failclosed"
for i in $(seq 1 60); do
  if kubectl -n "$ARGO_NS" get application presync-failclosed -o jsonpath='{.status.sync.status}' 2>/dev/null | grep -Eq 'OutOfSync|Synced'; then
    break
  fi
  sleep 2
done
sync_app "presync-failclosed"
wait_app_operation_phase "presync-failclosed" "Failed" 180
FC_PHASE=$(kubectl -n "$ARGO_NS" get application presync-failclosed -o jsonpath='{.status.operationState.phase}')
[[ "$FC_PHASE" == "Failed" || "$FC_PHASE" == "Error" ]]
# Live Deployment must not exist (never synced past hook)
FC_EXISTS=$(kubectl -n presync-failclosed get deploy demo-api -o name 2>/dev/null || true)
test -z "$FC_EXISTS"
hook_logs presync-failclosed "$OUT_ROOT/logs-failclosed.txt"
extract_decision_line "$OUT_ROOT/logs-failclosed.txt" | tee "$OUT_ROOT/failclosed-decision.txt"
grep -Eq 'decision=UNKNOWN|decision=BLOCK' "$OUT_ROOT/failclosed-decision.txt"
FC_MS=$(python3 -c 'import json; print(json.load(open("'"$OUT_ROOT"'/logs-failclosed.txt.decision.json")).get("durationMs",""))')
record_latency "$FC_MS"
echo "scenario=failclosed result=blocked gate_ms=$FC_MS" | tee -a "$OUT_ROOT/results.txt"

########################################
# (d) Break-glass override syncs; override visible in logs
########################################
echo "==> (d) break-glass override on risky path → Synced + override logged"
kubectl -n "$ARGO_NS" delete application presync-breakglass --ignore-not-found >/dev/null 2>&1 || true
kubectl delete ns presync-breakglass --ignore-not-found >/dev/null 2>&1 || true
for i in $(seq 1 60); do
  kubectl get ns presync-breakglass >/dev/null 2>&1 || break
  sleep 1
done
create_app "presync-breakglass" "examples/e2e-argo-presync/breakglass" "presync-breakglass" "breakglass"
for i in $(seq 1 60); do
  if kubectl -n "$ARGO_NS" get application presync-breakglass -o jsonpath='{.status.sync.status}' 2>/dev/null | grep -Eq 'OutOfSync|Synced'; then
    break
  fi
  sleep 2
done
sync_app "presync-breakglass"
wait_jsonpath "$ARGO_NS" "application/presync-breakglass" "{.status.sync.status}" "Synced" 180 "breakglass sync"
# Desired is intentionally unschedulable (nodeSelector); health may stay Progressing — assert sync + live spec.
BG_REP=$(kubectl -n presync-breakglass get deploy demo-api -o jsonpath='{.spec.replicas}')
test "$BG_REP" = "8"
BG_SEL=$(kubectl -n presync-breakglass get deploy demo-api -o jsonpath='{.spec.template.spec.nodeSelector.e2e\.rehearsal\.io/no-such-node}')
test "$BG_SEL" = "true"
hook_logs presync-breakglass "$OUT_ROOT/logs-breakglass.txt"
extract_decision_line "$OUT_ROOT/logs-breakglass.txt" | tee "$OUT_ROOT/breakglass-decision.txt"
grep -q 'decision=ALLOW' "$OUT_ROOT/breakglass-decision.txt"
grep -q 'BREAK-GLASS OVERRIDE' "$OUT_ROOT/logs-breakglass.txt"
grep -q 'INC-e2e emergency override' "$OUT_ROOT/logs-breakglass.txt"
python3 - <<PY
import json
o=json.load(open("$OUT_ROOT/logs-breakglass.txt.decision.json"))
assert o.get("override",{}).get("enabled") is True
assert "INC-e2e" in o.get("override",{}).get("reason","")
print("override_ok")
PY
BG_MS=$(python3 -c 'import json; print(json.load(open("'"$OUT_ROOT"'/logs-breakglass.txt.decision.json")).get("durationMs",""))')
record_latency "$BG_MS"
METRICS_SAFE_TOTAL=$((METRICS_SAFE_TOTAL+1))  # override path is an intentional allow
echo "scenario=breakglass result=override_allow gate_ms=$BG_MS" | tee -a "$OUT_ROOT/results.txt"

########################################
# Offline fixture matrix (same detectors as CI argo-impact) via argo-presync exit mapping
########################################
echo "==> offline fixture matrix through argo-presync (hook exit mapping)"
run_offline() {
  local name=$1 expect_decision=$2
  local dir="examples/e2e-argo/$name"
  local out="$OUT_ROOT/offline-$name"
  mkdir -p "$out"
  set +e
  "$BIN" argo-presync \
    --app "demo-$name" \
    --desired-dir "$dir/desired" \
    --baseline "$dir/baseline.json" \
    --skip-app-annotations \
    --out "$out" | tee "$out/decision.jsonl"
  local code=$?
  set -e
  dec=$(python3 -c 'import json,sys; print(json.loads(open(sys.argv[1]).read().strip()).get("decision",""))' "$out/decision.jsonl")
  ms=$(python3 -c 'import json,sys; print(json.loads(open(sys.argv[1]).read().strip()).get("durationMs",""))' "$out/decision.jsonl")
  record_latency "$ms"
  echo "offline=$name decision=$dec exit=$code expect=$expect_decision gate_ms=$ms" | tee -a "$OUT_ROOT/results.txt"
  test "$dec" = "$expect_decision"
  case "$expect_decision" in
    ALLOW|WARN) test "$code" -eq 0 ;;
    BLOCK) test "$code" -eq 3 ;;
    UNKNOWN) test "$code" -eq 4 ;;
    *) echo "bad expect $expect_decision"; return 1 ;;
  esac
  if [[ "$expect_decision" == "BLOCK" || "$expect_decision" == "UNKNOWN" ]]; then
    METRICS_RISKY_TOTAL=$((METRICS_RISKY_TOTAL+1))
    METRICS_RISKY_BLOCKED=$((METRICS_RISKY_BLOCKED+1))
  elif [[ "$expect_decision" == "ALLOW" ]]; then
    METRICS_SAFE_TOTAL=$((METRICS_SAFE_TOTAL+1))
  fi
}
run_offline unschedulable BLOCK
run_offline netpol-cut BLOCK
run_offline pdb-violation BLOCK
run_offline unknown-pdb-inventory UNKNOWN
run_offline benign ALLOW

########################################
# Metrics
########################################
python3 - <<PY | tee "$OUT_ROOT/metrics.txt"
safe_total=$METRICS_SAFE_TOTAL
safe_false=$METRICS_SAFE_FALSE_BLOCK
risky_total=$METRICS_RISKY_TOTAL
risky_blocked=$METRICS_RISKY_BLOCKED
latencies=[$(IFS=,; echo "${METRICS_LATENCIES[*]}")]
latencies=[x for x in latencies if x!=""]
risky_pct = (100.0 * risky_blocked / risky_total) if risky_total else 0.0
false_pct = (100.0 * safe_false / safe_total) if safe_total else 0.0
avg_ms = (sum(latencies)/len(latencies)) if latencies else 0.0
print(f"risky_syncs_total={risky_total}")
print(f"risky_syncs_blocked={risky_blocked}")
print(f"risky_blocked_pct={risky_pct:.1f}")
print(f"safe_syncs_total={safe_total}")
print(f"safe_false_blocked={safe_false}")
print(f"safe_false_blocked_pct={false_pct:.1f}")
print(f"hook_latency_samples_ms={latencies}")
print(f"hook_latency_avg_ms={avg_ms:.1f}")
if latencies:
    print(f"hook_latency_max_ms={max(latencies)}")
    print(f"hook_latency_min_ms={min(latencies)}")
assert risky_total >= 1 and risky_blocked == risky_total, (risky_blocked, risky_total)
assert safe_false == 0 and safe_total >= 1, (safe_false, safe_total)
print("metrics_ok")
PY

{
  echo "# Argo CD PreSync gate — kind e2e results"
  echo
  echo "Argo CD: \`$ARGO_VERSION\` · kind cluster: \`$CLUSTER\` · source: \`$REPO_URL@$REPO_REV\`"
  echo
  echo "## Results"
  echo
  echo '```'
  cat "$OUT_ROOT/results.txt"
  echo '```'
  echo
  echo "## Metrics"
  echo
  echo '```'
  cat "$OUT_ROOT/metrics.txt"
  echo '```'
} | tee "$OUT_ROOT/SUMMARY.md"

echo "OK kind-argo-presync-e2e"
cat "$OUT_ROOT/metrics.txt"
