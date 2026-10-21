# Argo CD PreSync change-impact gate

Kubernetes manifests that run `rehearsal argo-presync` as an Argo CD **PreSync** hook so risky GitOps changes are blocked inside the cluster before sync (not only in CI).

## Contents

| File | Role |
| ---- | ---- |
| `serviceaccount.yaml` | Non-root Job identity (PreSync hook) |
| `rbac.yaml` | **Default** namespaced Role/RoleBinding (`patch` for dryRun=All) as PreSync hooks |
| `rbac-nodes.yaml` | **Default** ClusterRole (Nodes + Pods get/list/watch) + **namespace-unique** ClusterRoleBinding — normal resources, **not** hooks |
| `rbac-cluster.yaml` | **Opt-in** ClusterRole for namespaces/PVs + Applications (break-glass); Helm `argoPresync.clusterRBAC=true` |
| `job.yaml` | `argocd.argoproj.io/hook: PreSync`, `hook-delete-policy: BeforeHookCreation` |
| `kustomization.yaml` | Kustomize entrypoint; rewrites Binding name to `…-in-<namespace>` |

Provide ConfigMap `rehearsal-argo-presync-desired` with the proposed app YAML (mounted at `/gate/desired`). Set `namespace:` in kustomization (or Helm release namespace) to the Application destination — the ClusterRoleBinding becomes `rehearsal-argo-presync-nodes-in-<ns>` so a second-namespace install does not overwrite the first.

## AppProject `clusterResourceWhitelist`

ClusterRole / ClusterRoleBinding are **not** created by PreSync hooks (they are normal Application resources). Strict AppProjects must still allow them on the Application itself:

```yaml
clusterResourceWhitelist:
  - group: rbac.authorization.k8s.io
    kind: ClusterRole
  - group: rbac.authorization.k8s.io
    kind: ClusterRoleBinding
```

Hook-only whitelists are no longer required for these objects.

## Helm

```bash
helm template r deploy/helm/architecture-rehearsal \
  --set api.token=dev \
  --set argoPresync.enabled=true
```

## Image

```bash
docker build -f Dockerfile.presync -t ghcr.io/justrunme/architecture-rehearsal-presync:1.8.0 .
```
