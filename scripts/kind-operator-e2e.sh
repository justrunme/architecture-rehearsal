#!/usr/bin/env bash
# Kind operator E2E (v1.5.3). Requires: kind, kubectl, docker, helm.
# CI: operator-kind-e2e job.
# Installs via Helm chart (CRD + control plane + operator), then asserts full lifecycle.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"
CLUSTER="${KIND_CLUSTER:-rehearsal-e2e}"
RELEASE="${HELM_RELEASE:-rehearsal}"
NS="${HELM_NAMESPACE:-default}"
TOKEN="e2e-$(openssl rand -hex 16)"
CP_IMAGE="architecture-rehearsal:e2e"
OP_IMAGE="architecture-rehearsal-operator:e2e"

need() { command -v "$1" >/dev/null || { echo "missing required tool: $1"; exit 1; }; }
need kind
need kubectl
need docker
need helm

cleanup() {
  kind delete cluster --name "$CLUSTER" 2>/dev/null || true
}
trap cleanup EXIT

dump() {
  echo "==> DIAG"
  kubectl get crd rehearsalruns.rehearsal.io 2>/dev/null || true
  kubectl get all,cm,secret -l 'app.kubernetes.io/name in (architecture-rehearsal,rehearsal-operator)' -o wide 2>/dev/null || true
  kubectl get deploy,pods -o wide 2>/dev/null || true
  kubectl describe rehearsalrun e2e-run 2>/dev/null || true
  kubectl logs -l app.kubernetes.io/name=rehearsal-operator --all-containers --tail=80 2>/dev/null || true
  kubectl logs -l app.kubernetes.io/name=architecture-rehearsal --all-containers --tail=80 2>/dev/null || true
  kubectl get events --sort-by=.lastTimestamp 2>/dev/null | tail -40 || true
}

echo "==> kind create $CLUSTER"
kind create cluster --name "$CLUSTER"

echo "==> build + load images (no provenance — kind load)"
docker build --provenance=false --sbom=false -t "$CP_IMAGE" -f Dockerfile .
docker build --provenance=false --sbom=false -t "$OP_IMAGE" -f Dockerfile.operator .
kind load docker-image "$CP_IMAGE" --name "$CLUSTER"
kind load docker-image "$OP_IMAGE" --name "$CLUSTER"

echo "==> fixture ConfigMap"
kubectl create configmap rehearsal-fixtures \
  --from-file=baseline.json=examples/golden/rwo-node-loss/baseline.json \
  --from-file=change.json=examples/golden/rwo-node-loss/change.json \
  --from-file=change2.json=examples/golden/rwo-node-loss/change.json \
  -n "$NS"

echo "==> helm upgrade --install $RELEASE (chart CRD + control plane + operator)"
# Workdir defaults to /var/lib/rehearsal; mount golden fixtures under it.
# Apply without --wait so we can dump pods if readiness fails.
helm upgrade --install "$RELEASE" deploy/helm/architecture-rehearsal \
  --namespace "$NS" \
  --set api.token="$TOKEN" \
  --set api.org=default \
  --set api.async=true \
  --set api.workers=1 \
  --set persistence.enabled=false \
  --set image.repository=architecture-rehearsal \
  --set image.tag=e2e \
  --set image.pullPolicy=Never \
  --set operator.enabled=true \
  --set operator.replicas=1 \
  --set operator.leaderElection=true \
  --set operator.networkPolicy.enabled=false \
  --set operator.image.repository=architecture-rehearsal-operator \
  --set operator.image.tag=e2e \
  --set operator.image.pullPolicy=Never \
  --set-json 'extraVolumes=[{"name":"fixtures","configMap":{"name":"rehearsal-fixtures"}}]' \
  --set-json 'extraVolumeMounts=[{"name":"fixtures","mountPath":"/var/lib/rehearsal/baseline.json","subPath":"baseline.json"},{"name":"fixtures","mountPath":"/var/lib/rehearsal/change.json","subPath":"change.json"},{"name":"fixtures","mountPath":"/var/lib/rehearsal/change2.json","subPath":"change2.json"}]'

echo "==> verify Helm installed CRD + resources"
kubectl get crd rehearsalruns.rehearsal.io
kubectl get deploy,pods,svc -o wide
if ! kubectl wait --for=condition=available deploy -l app.kubernetes.io/name=architecture-rehearsal --timeout=180s; then
  dump
  exit 1
fi
if ! kubectl wait --for=condition=available deploy -l app.kubernetes.io/name=rehearsal-operator --timeout=180s; then
  dump
  exit 1
fi

echo "==> create RehearsalRun gen1 (golden rwo-node-loss → expect decision=block)"
kubectl apply -f - <<'EOF'
apiVersion: rehearsal.io/v1beta1
kind: RehearsalRun
metadata:
  name: e2e-run
spec:
  baselineRef: baseline.json
  changeRef: change.json
  async: true
EOF

wait_fields() {
  # wait_fields <timeout_sec> <description> <bash-condition using PHASE/DECISION/JOB/RUNID/GEN/EVID/READY>
  local timeout=$1 desc=$2
  shift 2
  local cond=$1
  local i=0 max=$((timeout / 2))
  for i in $(seq 1 "$max"); do
    PHASE=$(kubectl get rehearsalrun e2e-run -o jsonpath='{.status.phase}' 2>/dev/null || true)
    DECISION=$(kubectl get rehearsalrun e2e-run -o jsonpath='{.status.decision}' 2>/dev/null || true)
    JOB=$(kubectl get rehearsalrun e2e-run -o jsonpath='{.status.jobId}' 2>/dev/null || true)
    RUNID=$(kubectl get rehearsalrun e2e-run -o jsonpath='{.status.controlPlaneRunId}' 2>/dev/null || true)
    GEN=$(kubectl get rehearsalrun e2e-run -o jsonpath='{.status.observedGeneration}' 2>/dev/null || true)
    EVID=$(kubectl get rehearsalrun e2e-run -o jsonpath='{.status.evidenceDigest}' 2>/dev/null || true)
    READY=$(kubectl get rehearsalrun e2e-run -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}' 2>/dev/null || true)
    MSG=$(kubectl get rehearsalrun e2e-run -o jsonpath='{.status.message}' 2>/dev/null || true)
    echo "  [$i] phase=$PHASE decision=$DECISION job=${JOB:0:12}… runId=$RUNID gen=$GEN ready=$READY evid=${EVID:0:12}… msg=${MSG:0:60}"
    if eval "$cond"; then
      return 0
    fi
    sleep 2
  done
  echo "FAIL: timeout waiting for $desc"
  dump
  return 1
}

echo "==> wait jobId + runId (gen1)"
wait_fields 90 "gen1 job+runId" '[[ -n "$JOB" && -n "$RUNID" && "$GEN" == "1" && "$RUNID" == default-e2e-run-*-g1 ]]'
JOB1=$JOB
RUNID1=$RUNID
echo "gen1 enqueued: job=$JOB1 runId=$RUNID1"

echo "==> wait terminal outcome (phase Completed, Ready=True, decision=block, evidenceDigest)"
# Without observedRef, engine completes after gate with decision from analyze (block for rwo-node-loss).
wait_fields 180 "terminal block" '[[ "$PHASE" == "Completed" && "$READY" == "True" && "$DECISION" == "block" && -n "$EVID" ]]'
echo "gen1 terminal OK: phase=$PHASE decision=$DECISION evidenceDigest=$EVID"

echo "==> patch changeRef → generation 2"
kubectl patch rehearsalrun e2e-run --type=merge -p '{"spec":{"changeRef":"change2.json"}}'
wait_fields 90 "gen2 run" '[[ "$GEN" == "2" && "$RUNID" == *-g2 && -n "$JOB" && "$JOB" != "'"$JOB1"'" ]]'
JOB2=$JOB
RUNID2=$RUNID
echo "gen2 enqueued: job=$JOB2 runId=$RUNID2"

echo "==> wait gen2 terminal"
wait_fields 180 "gen2 terminal" '[[ "$GEN" == "2" && "$PHASE" == "Completed" && "$READY" == "True" && "$DECISION" == "block" && -n "$EVID" ]]'
echo "gen2 terminal OK"

echo "==> restart operator — no duplicate job on same generation"
OP_DEPLOY=$(kubectl get deploy -l app.kubernetes.io/name=rehearsal-operator -o jsonpath='{.items[0].metadata.name}')
kubectl rollout restart "deploy/${OP_DEPLOY}"
kubectl rollout status "deploy/${OP_DEPLOY}" --timeout=120s
sleep 8
J3=$(kubectl get rehearsalrun e2e-run -o jsonpath='{.status.jobId}')
OG=$(kubectl get rehearsalrun e2e-run -o jsonpath='{.status.observedGeneration}')
if [[ "$OG" == "2" && "$J3" != "$JOB2" ]]; then
  echo "FAIL: jobId changed after restart on same gen: $JOB2 -> $J3"
  dump
  exit 1
fi
echo "restart OK: jobId stable=$J3"

echo "==> scale operator to 2 replicas (leader election)"
kubectl scale "deploy/${OP_DEPLOY}" --replicas=2
kubectl rollout status "deploy/${OP_DEPLOY}" --timeout=120s
sleep 5
kubectl get pods -l app.kubernetes.io/name=rehearsal-operator
J4=$(kubectl get rehearsalrun e2e-run -o jsonpath='{.status.jobId}')
[[ "$J4" == "$J3" ]] || { echo "FAIL: jobId changed under 2 replicas"; dump; exit 1; }

echo "==> kind operator helm e2e PASSED"
