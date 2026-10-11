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
  kind delete cluster --name "$CLUSTER" 2>/dev/null || true
}
trap cleanup EXIT

mkdir -p "$OUT_ROOT"
: > "$OUT_ROOT/results.txt"
: > "$OUT_ROOT/metrics.txt"

echo "==> unit-level smoke: build + go test presync"
go test ./internal/presync/ -count=1
go build -o "$BIN" ./cmd/rehearsal

echo "==> kind create $CLUSTER"
kind create cluster --name "$CLUSTER"

echo "==> build + load PreSync image $IMAGE"
docker build --provenance=false --sbom=false -t "$IMAGE" -f Dockerfile.presync .
kind load docker-image "$IMAGE" --name "$CLUSTER"

echo "==> install Argo CD $ARGO_VERSION"
kubectl create namespace "$ARGO_NS"
kubectl apply -n "$ARGO_NS" -f "https://raw.githubusercontent.com/argoproj/argo-cd/${ARGO_VERSION}/manifests/install.yaml"
kubectl -n "$ARGO_NS" wait --for=condition=Available deploy/argocd-server --timeout=300s
kubectl -n "$ARGO_NS" wait --for=condition=Available deploy/argocd-repo-server --timeout=300s
kubectl -n "$ARGO_NS" rollout status statefulset/argocd-application-controller --timeout=300s

echo "==> local git repo + daemon for Application sources"
GIT_PARENT="$OUT_ROOT/git-parent"
rm -rf "$GIT_PARENT"
mkdir -p "$GIT_PARENT/presync-src"
cp -a examples/e2e-argo-presync "$GIT_PARENT/presync-src/examples"
# Strip nested git if any; init publish repo
(
  cd "$GIT_PARENT/presync-src"
  git init -b main
  git config user.email "e2e@rehearsal.local"
  git config user.name "e2e"
  # Keep only e2e-argo-presync tree at repo root path examples/e2e-argo-presync
  git add examples
  git commit -m "presync e2e fixtures"
)
git clone --bare "$GIT_PARENT/presync-src" "$GIT_PARENT/presync.git"
git -C "$GIT_PARENT/presync.git" update-server-info
git daemon --reuseaddr --export-all --base-path="$GIT_PARENT" --verbose --listen=0.0.0.0 --port=9418 &
GIT_DAEMON_PID=$!
sleep 1
KIND_GW=$(docker network inspect kind -f '{{(index .IPAM.Config 0).Gateway}}')
REPO_URL="git://${KIND_GW}:9418/presync"
echo "  REPO_URL=$REPO_URL"
# Smoke clone from host
git ls-remote "$REPO_URL" HEAD | tee "$OUT_ROOT/git-ls-remote.txt"
test -s "$OUT_ROOT/git-ls-remote.txt"

# Allow git:// from kind network (repo-server egress to host gateway).
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
    echo "  [$i] operation.phase=${phase:-<empty>} msg=${msg:0:120}"
    if [[ "$phase" == "$want" ]]; then
      return 0
    fi
    # Also accept Error when want is Failed (Argo versions differ)
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
  if [[ "$breakglass" == "breakglass" ]]; then
    cat <<EOF | kubectl apply -f -
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: ${name}
  namespace: ${ARGO_NS}
  annotations:
    rehearsal.io/break-glass: "true"
    rehearsal.io/break-glass-reason: "INC-e2e emergency override"
spec:
  project: default
  source:
    repoURL: ${REPO_URL}
    targetRevision: HEAD
    path: ${path}
  destination:
    server: https://kubernetes.default.svc
    namespace: ${dest_ns}
  syncPolicy:
    syncOptions:
      - CreateNamespace=true
EOF
  else
    cat <<EOF | kubectl apply -f -
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: ${name}
  namespace: ${ARGO_NS}
spec:
  project: default
  source:
    repoURL: ${REPO_URL}
    targetRevision: HEAD
    path: ${path}
  destination:
    server: https://kubernetes.default.svc
    namespace: ${dest_ns}
  syncPolicy:
    syncOptions:
      - CreateNamespace=true
EOF
  fi
}

sync_app() {
  local name=$1
  # Trigger sync (manual). Prefer argocd CLI if present; else patch operation.
  kubectl -n "$ARGO_NS" patch application "$name" --type merge -p \
    "{\"operation\":{\"initiatedBy\":{\"username\":\"e2e\"},\"sync\":{\"revision\":\"HEAD\"}}}"
}

hook_logs() {
  local dest_ns=$1
  local out=$2
  # Find PreSync job pods
  kubectl -n "$dest_ns" get jobs,pods -l app.kubernetes.io/name=rehearsal-argo-presync -o wide | tee "$out.list" || true
  local pod
  pod=$(kubectl -n "$dest_ns" get pods -l app.kubernetes.io/name=rehearsal-argo-presync --sort-by=.metadata.creationTimestamp -o jsonpath='{.items[-1:].metadata.name}' 2>/dev/null || true)
  if [[ -n "$pod" ]]; then
    kubectl -n "$dest_ns" logs "$pod" -c gate | tee "$out" || true
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
create_app "presync-safe" "examples/e2e-argo-presync/safe" "presync-demo"
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
# false block would mean sync did not become Synced — we already asserted Synced
echo "scenario=safe result=pass sync_ms=$((SYNC_END-SYNC_START)) gate_ms=$SAFE_MS" | tee -a "$OUT_ROOT/results.txt"

########################################
# (b) Risky change blocked; live object NOT changed
########################################
echo "==> (b) risky PreSync BLOCK — live Deployment unchanged"
# Point same Application at risky path
BEFORE_REP=$(kubectl -n presync-demo get deploy demo-api -o jsonpath='{.spec.replicas}')
BEFORE_SEL=$(kubectl -n presync-demo get deploy demo-api -o jsonpath='{.spec.template.spec.nodeSelector.e2e\.rehearsal\.io/no-such-node}')
test "$BEFORE_REP" = "1"
test -z "$BEFORE_SEL"
kubectl -n "$ARGO_NS" patch application presync-safe --type merge -p \
  '{"metadata":{"name":"presync-safe"},"spec":{"source":{"path":"examples/e2e-argo-presync/risky"}}}'
# Rename tracking: keep name presync-safe but path risky
sync_app "presync-safe"
wait_app_operation_phase "presync-safe" "Failed" 180
PHASE=$(kubectl -n "$ARGO_NS" get application presync-safe -o jsonpath='{.status.operationState.phase}')
[[ "$PHASE" == "Failed" || "$PHASE" == "Error" ]]
kubectl -n "$ARGO_NS" get application presync-safe -o yaml | tee "$OUT_ROOT/app-risky-blocked.yaml" >/dev/null
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
create_app "presync-failclosed" "examples/e2e-argo-presync/failclosed" "presync-failclosed"
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
create_app "presync-breakglass" "examples/e2e-argo-presync/breakglass" "presync-breakglass" "breakglass"
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
  echo "offline=$name decision=$dec exit=$code expect=$expect_decision" | tee -a "$OUT_ROOT/results.txt"
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
# Hard asserts on fixture matrix + kind scenarios
assert risky_total >= 1 and risky_blocked == risky_total, (risky_blocked, risky_total)
assert safe_false == 0 and safe_total >= 1, (safe_false, safe_total)
print("metrics_ok")
PY

{
  echo "# Argo CD PreSync gate — kind e2e results"
  echo
  echo "Argo CD: \`$ARGO_VERSION\` · kind cluster: \`$CLUSTER\`"
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
