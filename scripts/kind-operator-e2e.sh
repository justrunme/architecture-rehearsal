#!/usr/bin/env bash
# Kind operator E2E (v1.5.1). Requires: kind, kubectl, docker, go.
# Optional in CI when KIND_E2E=1.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"
CLUSTER="${KIND_CLUSTER:-rehearsal-e2e}"
NS=default
TOKEN="e2e-$(openssl rand -hex 16)"

cleanup() {
  kind delete cluster --name "$CLUSTER" 2>/dev/null || true
}
trap cleanup EXIT

echo "==> kind create $CLUSTER"
kind create cluster --name "$CLUSTER"

echo "==> build images"
docker build -t architecture-rehearsal:e2e -f Dockerfile .
docker build -t architecture-rehearsal-operator:e2e -f Dockerfile.operator .
kind load docker-image architecture-rehearsal:e2e --name "$CLUSTER"
kind load docker-image architecture-rehearsal-operator:e2e --name "$CLUSTER"

echo "==> CRD"
kubectl apply -f config/crd/rehearsal.io_rehearsalruns.yaml

echo "==> control plane (serve)"
kubectl create secret generic rehearsal-api --from-literal=token="$TOKEN"
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
          args: ["serve", "--addr", ":8080", "--workdir", "/data", "--memory", "--async"]
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
      volumes:
        - name: data
          emptyDir: {}
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

kubectl wait --for=condition=available deploy/architecture-rehearsal --timeout=120s

echo "==> operator"
kubectl create secret generic rehearsal-operator-token --from-literal=token="$TOKEN"
# Patch deploy to use e2e images and in-cluster URL
sed \
  -e 's|ghcr.io/justrunme/architecture-rehearsal-operator:1.5.1|architecture-rehearsal-operator:e2e|g' \
  -e 's|replicas: 2|replicas: 1|g' \
  -e 's|--leader-elect=true|--leader-elect=false|g' \
  config/operator/serviceaccount.yaml \
  config/operator/clusterrole.yaml \
  config/operator/clusterrolebinding.yaml \
  config/operator/deployment.yaml | kubectl apply -f -

kubectl wait --for=condition=available deploy/rehearsal-operator --timeout=120s

echo "==> create RehearsalRun"
# Place dummy refs — memory backend accepts create without files existing for API
kubectl apply -f - <<'EOF'
apiVersion: rehearsal.io/v1beta1
kind: RehearsalRun
metadata:
  name: e2e-run
spec:
  baselineRef: b.json
  changeRef: c.json
  async: true
EOF

echo "==> wait for status"
for i in $(seq 1 30); do
  PHASE=$(kubectl get rehearsalrun e2e-run -o jsonpath='{.status.phase}' 2>/dev/null || true)
  JOB=$(kubectl get rehearsalrun e2e-run -o jsonpath='{.status.jobId}' 2>/dev/null || true)
  GEN=$(kubectl get rehearsalrun e2e-run -o jsonpath='{.status.observedGeneration}' 2>/dev/null || true)
  RUNID=$(kubectl get rehearsalrun e2e-run -o jsonpath='{.status.controlPlaneRunId}' 2>/dev/null || true)
  echo "  attempt $i phase=$PHASE job=$JOB gen=$GEN runId=$RUNID"
  if [[ -n "$RUNID" && -n "$GEN" ]]; then
    break
  fi
  sleep 2
done

RUNID=$(kubectl get rehearsalrun e2e-run -o jsonpath='{.status.controlPlaneRunId}')
[[ "$RUNID" == "default-e2e-run-g1" || "$RUNID" == default-e2e-run-g* ]] || {
  echo "unexpected run id: $RUNID"
  kubectl describe rehearsalrun e2e-run || true
  kubectl logs deploy/rehearsal-operator --tail=50 || true
  exit 1
}

echo "==> operator restart — no duplicate job"
JOB1=$(kubectl get rehearsalrun e2e-run -o jsonpath='{.status.jobId}')
kubectl rollout restart deploy/rehearsal-operator
kubectl rollout status deploy/rehearsal-operator --timeout=90s
sleep 5
JOB2=$(kubectl get rehearsalrun e2e-run -o jsonpath='{.status.jobId}')
if [[ -n "$JOB1" && -n "$JOB2" && "$JOB1" != "$JOB2" ]]; then
  echo "jobId changed after restart: $JOB1 -> $JOB2 (duplicate enqueue?)"
  # soft fail warning only if both set and different on same generation
  GEN=$(kubectl get rehearsalrun e2e-run -o jsonpath='{.status.observedGeneration}')
  if [[ "$GEN" == "1" ]]; then
    echo "FAIL: duplicate job on same generation"
    exit 1
  fi
fi

echo "==> kind operator e2e OK"
