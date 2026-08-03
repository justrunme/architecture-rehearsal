# Kubernetes Operator (v1.5.2)

The `rehearsal-operator` reconciles `RehearsalRun` CRs against the Architecture Rehearsal **control plane HTTP API**.

## Architecture

```text
RehearsalRun CR
      │
      ▼
controller-runtime operator  ──token/URL from Deployment only──►  control plane (serve)
      │                                                              │
      └── status.phase / jobId / evidenceDigest ◄────────────────────┘
```

**Trust boundary:** `REHEARSAL_API_URL` and `REHEARSAL_API_TOKEN` are set only on the operator Deployment (Secret).  
They are **never** taken from the CR. `spec.controlPlaneURL` was removed in v1.5.1.

## Install

### CRD

```bash
kubectl apply -f config/crd/rehearsal.io_rehearsalruns.yaml
```

### Operator manifests

```bash
kubectl create secret generic rehearsal-operator-token \
  --from-literal=token="$REHEARSAL_API_TOKEN"
# Edit REHEARSAL_API_URL in config/operator/deployment.yaml if needed
kubectl apply -k config/operator/
```

### Helm

```bash
helm upgrade --install rehearsal deploy/helm/architecture-rehearsal \
  --set api.token="$REHEARSAL_API_TOKEN" \
  --set image.tag=1.5.2 \
  --set operator.enabled=true \
  --set operator.image.tag=1.5.2
# CRD is installed from chart crds/ automatically
kubectl get crd rehearsalruns.rehearsal.io
```

Default: `operator.enabled=false` (no cluster-wide RBAC until you opt in).  
Default: `operator.networkPolicy.enabled=false` (enable only with real `kubeAPIServerCIDRs`).

## CR example

See `examples/operator/rehearsalrun.yaml`.

```yaml
apiVersion: rehearsal.io/v1beta1
kind: RehearsalRun
metadata:
  name: payments-rollout
spec:
  baselineRef: baselines/prod.json
  changeRef: changes/payments-v2.json
  async: true
```

## Status semantics

| Field | Meaning |
| ----- | ------- |
| `status.observedGeneration` | Last reconciled `metadata.generation` |
| `status.specDigest` | sha256 of Spec |
| `status.controlPlaneRunId` | `{namespace}-{name}-{uid8}-g{generation}` |
| `status.jobId` | Async job id (set once per generation) |
| `status.evidenceDigest` | Chain/report digest when available |
| `status.phase` | Control-plane phase |

### Generation / immutability

Changing Spec bumps `metadata.generation`. The operator creates a **new** control-plane run id  
`…-g2`, `…-g3`, … instead of overwriting the previous run’s artifacts.

### Conditions

- `Accepted` — CR accepted by operator  
- `Running` — advance in progress  
- `Ready` — terminal success (or false while in progress)  
- `Failed` — terminal failure or reconcile error  

`lastTransitionTime` updates only when status/reason/message change.

## Troubleshooting

| Symptom | Check |
| ------- | ----- |
| `REHEARSAL_API_URL required` | Operator Deployment env; not CR |
| 401 from control plane | Secret `token` matches `serve` token |
| Conflict immutable | Spec change reused an old run id; delete CR or use new name |
| No jobId | Sync advance (`async: false`) or advance not yet called |

## Security

See [operator-security.md](./operator-security.md).
