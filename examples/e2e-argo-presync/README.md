# Argo CD PreSync change-impact gate fixtures

GitOps Application sources used by `scripts/kind-argo-presync-e2e.sh`.

| Path | Intent |
| ---- | ------ |
| `safe/` | Benign Deployment — PreSync ALLOW, sync becomes Healthy |
| `risky/` | Unschedulable replicas — PreSync BLOCK, live object unchanged |
| `failclosed/` | Gate cannot compute (missing desired input) — sync aborted |

Break-glass is exercised by annotating the Application CR (not these paths):

```yaml
metadata:
  annotations:
    rehearsal.io/break-glass: "true"
    rehearsal.io/break-glass-reason: "INC-e2e emergency"
```

Each path includes the PreSync Job (`argocd.argoproj.io/hook: PreSync`) and a ConfigMap of desired manifests the Job evaluates against the live cluster.
