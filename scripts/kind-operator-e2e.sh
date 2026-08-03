#!/usr/bin/env bash
# Kind operator E2E (v1.5.2). Requires: kind, kubectl, docker.
# CI: operator-kind-e2e job.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"
CLUSTER="${KIND_CLUSTER:-rehearsal-e2e}"
TOKEN="e2e-$(openssl rand -hex 16)"

cleanup() {
  kind delete cluster --name "$CLUSTER" 2>/dev/null || true
}
trap cleanup EXIT

echo "==> kind create $CLUSTER"
kind create cluster --name "$CLUSTER"

echo "==> build + load images"
docker build -t architecture-rehearsal:e2e -f Dockerfile .
docker build -t architecture-rehearsal-operator:e2e -f Dockerfile.operator .
kind load docker-image architecture-rehearsal:e2e --name "$CLUSTER"
kind load docker-image architecture-rehearsal-operator:e2e --name "$CLUSTER"

echo "==> CRD"
kubectl apply -f config/crd/rehearsal.io_rehearsalruns.yaml

echo "==> control plane with SQLite (not memory) + real fixtures"
kubectl create secret generic rehearsal-api --from-literal=token="$TOKEN"
# ConfigMap with golden fixtures
kubectl create configmap rehearsal-fixtures \
  --from-file=baseline.json=examples/golden/rwo-node-loss/baseline.json \
  --from-file=change.json=examples/golden/rwo-node-loss/change.json \
  --from-file=change2.json=examples/golden/rwo-node-loss/change.json

cat <<EOF | kubectl apply -f -
apiVersion: apps/v1
kind: Deployment
metadata:
  name: architecture-rehearsal
  labels:
    app.kubernetes.io/name: architecture-rehearsal
spec:
  replicas: 1
  selector:
    matchLabels:
      app.kubernetes.io/name: architecture-rehearsal
  template:
    metadata:
      labels:
        app.kubernetes.io/name: architecture-rehearsal
    spec:
      containers:
        - name: api
          image: architecture-rehearsal:e2e
          imagePullPolicy: Never
          args:
            - serve
            - --addr=:8080
            - --workdir=/data
            - --db=/data/rehearsal.db
            - --blob=/data/blobs
            - --async
            - --workers=1
          env:
            - name: REHEARSAL_API_TOKEN
              valueFrom:
                secretKeyRef:
                  name: rehearsal-api
                  key: token
            - name: REHEARSAL_API_ORG
              value: default
          ports:
            - containerPort: 8080
          volumeMounts:
            - name: data
              mountPath: /data
            - name: fixtures
              mountPath: /data/baseline.json
              subPath: baseline.json
            - name: fixtures
              mountPath: /data/change.json
              subPath: change.json
            - name: fixtures
              mountPath: /data/change2.json
              subPath: change2.json
      volumes:
        - name: data
          emptyDir: {}
        - name: fixtures
          configMap:
            name: rehearsal-fixtures
---
apiVersion: v1
kind: Service
metadata:
  name: architecture-rehearsal
spec:
  selector:
    app.kubernetes.io/name: architecture-rehearsal
  ports:
    - port: 8080
      targetPort: 8080
EOF

kubectl wait --for=condition=available deploy/architecture-rehearsal --timeout=180s

echo "==> operator (2 replicas, leader election ON, no NetworkPolicy)"
kubectl create secret generic rehearsal-operator-token --from-literal=token="$TOKEN"
# Apply each manifest separately — concatenating YAML without '---' merges
# documents and leaves roleRef/rules/subjects on the Deployment (strict decode fail).
for f in \
  config/operator/serviceaccount.yaml \
  config/operator/clusterrole.yaml \
  config/operator/clusterrolebinding.yaml \
  config/operator/deployment.yaml
do
  sed \
    -e 's|ghcr.io/justrunme/architecture-rehearsal-operator:1.5.2|architecture-rehearsal-operator:e2e|g' \
    -e 's|imagePullPolicy: IfNotPresent|imagePullPolicy: Never|g' \
    "$f" | kubectl apply -f -
done

kubectl wait --for=condition=available deploy/rehearsal-operator --timeout=180s
# ensure 2 pods
kubectl get pods -l app.kubernetes.io/name=rehearsal-operator

echo "==> create RehearsalRun gen1"
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

wait_status() {
  local want_field=$1 want_prefix=$2
  for i in $(seq 1 45); do
    val=$(kubectl get rehearsalrun e2e-run -o jsonpath="{${want_field}}" 2>/dev/null || true)
    echo "  [$i] ${want_field}=${val}"
    if [[ -n "$val" && "$val" == ${want_prefix}* ]]; then
      return 0
    fi
    if [[ -n "$val" && -z "$want_prefix" ]]; then
      return 0
    fi
    sleep 2
  done
  return 1
}

echo "==> wait jobId + runId"
JOB=""
RUNID=""
for i in $(seq 1 45); do
  JOB=$(kubectl get rehearsalrun e2e-run -o jsonpath='{.status.jobId}' 2>/dev/null || true)
  RUNID=$(kubectl get rehearsalrun e2e-run -o jsonpath='{.status.controlPlaneRunId}' 2>/dev/null || true)
  GEN=$(kubectl get rehearsalrun e2e-run -o jsonpath='{.status.observedGeneration}' 2>/dev/null || true)
  echo "  [$i] job=$JOB runId=$RUNID gen=$GEN"
  if [[ -n "$JOB" && -n "$RUNID" && "$GEN" == "1" ]]; then
    break
  fi
  sleep 2
done

if [[ -z "$JOB" ]]; then
  echo "FAIL: jobId empty"
  kubectl describe rehearsalrun e2e-run || true
  kubectl logs -l app.kubernetes.io/name=rehearsal-operator --tail=80 || true
  kubectl logs deploy/architecture-rehearsal --tail=40 || true
  exit 1
fi
if [[ "$RUNID" != default-e2e-run-*-g1 ]]; then
  echo "FAIL: unexpected run id $RUNID (want ...-g1 with uid)"
  exit 1
fi
echo "gen1 OK: job=$JOB runId=$RUNID"

echo "==> patch changeRef → generation 2"
kubectl patch rehearsalrun e2e-run --type=merge -p '{"spec":{"changeRef":"change2.json"}}'
# wait for generation
for i in $(seq 1 30); do
  G=$(kubectl get rehearsalrun e2e-run -o jsonpath='{.metadata.generation}')
  OG=$(kubectl get rehearsalrun e2e-run -o jsonpath='{.status.observedGeneration}')
  RID=$(kubectl get rehearsalrun e2e-run -o jsonpath='{.status.controlPlaneRunId}')
  J2=$(kubectl get rehearsalrun e2e-run -o jsonpath='{.status.jobId}')
  echo "  [$i] metadata.generation=$G observed=$OG runId=$RID job=$J2"
  if [[ "$G" == "2" && "$OG" == "2" && "$RID" == *-g2 ]]; then
    break
  fi
  sleep 2
done
RID=$(kubectl get rehearsalrun e2e-run -o jsonpath='{.status.controlPlaneRunId}')
J2=$(kubectl get rehearsalrun e2e-run -o jsonpath='{.status.jobId}')
[[ "$RID" == *-g2 ]] || { echo "FAIL: expected g2 run id got $RID"; exit 1; }
[[ -n "$J2" ]] || { echo "FAIL: jobId empty on gen2"; exit 1; }
[[ "$J2" != "$JOB" ]] || { echo "FAIL: gen2 jobId same as gen1"; exit 1; }
echo "gen2 OK: runId=$RID job=$J2"

echo "==> restart operator — no duplicate job on same generation"
kubectl rollout restart deploy/rehearsal-operator
kubectl rollout status deploy/rehearsal-operator --timeout=120s
sleep 8
J3=$(kubectl get rehearsalrun e2e-run -o jsonpath='{.status.jobId}')
OG=$(kubectl get rehearsalrun e2e-run -o jsonpath='{.status.observedGeneration}')
if [[ "$OG" == "2" && "$J3" != "$J2" ]]; then
  echo "FAIL: jobId changed after restart on same gen: $J2 -> $J3"
  exit 1
fi
echo "restart OK: jobId stable=$J3"

echo "==> scale operator to 2 replicas (leader election)"
kubectl scale deploy/rehearsal-operator --replicas=2
kubectl rollout status deploy/rehearsal-operator --timeout=120s
sleep 5
kubectl get pods -l app.kubernetes.io/name=rehearsal-operator
J4=$(kubectl get rehearsalrun e2e-run -o jsonpath='{.status.jobId}')
[[ "$J4" == "$J3" ]] || { echo "FAIL: jobId changed under 2 replicas"; exit 1; }

echo "==> kind operator e2e PASSED"
