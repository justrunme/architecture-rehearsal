# Argo CD PreSync change-impact gate

Kubernetes manifests that run `rehearsal argo-presync` as an Argo CD **PreSync** hook so risky GitOps changes are blocked inside the cluster before sync (not only in CI).

## Contents

| File | Role |
| ---- | ---- |
| `serviceaccount.yaml` | Non-root Job identity |
| `rbac.yaml` | Read-only ClusterRole (inventory + Application get for break-glass) |
| `job.yaml` | `argocd.argoproj.io/hook: PreSync`, `hook-delete-policy: BeforeHookCreation` |
| `kustomization.yaml` | Kustomize entrypoint |

Provide ConfigMap `rehearsal-argo-presync-desired` with the proposed app YAML (mounted at `/gate/desired`). Patch the ClusterRoleBinding subject namespace to the Application destination namespace.

## Helm

```bash
helm template r deploy/helm/architecture-rehearsal \
  --set api.token=dev \
  --set argoPresync.enabled=true
```

## Image

```bash
docker build -f Dockerfile.presync -t ghcr.io/justrunme/architecture-rehearsal-presync:1.6.0 .
```
