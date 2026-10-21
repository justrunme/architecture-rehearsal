# Argo CD PreSync change-impact gate

Kubernetes manifests that run `rehearsal argo-presync` as an Argo CD **PreSync** hook so risky GitOps changes are blocked inside the cluster before sync (not only in CI).

## Contents

| File | Role |
| ---- | ---- |
| `base/` | ServiceAccount, Role/RoleBinding (hooks), per-ns ClusterRole/Binding (Nodes + Pods list), Job |
| `base/rbac-cluster.yaml` | **Opt-in** per-namespace ClusterRole for namespaces/PVs + Applications; Helm `argoPresync.clusterRBAC=true` |
| `overlays/example/` | Documented install: set `namespace:` + replacements → `…-in-<ns>` |

## Install (kustomize overlay)

Do **not** only edit `namespace:` on a remote base that already ran replacements. Use an overlay so the rename runs with your destination namespace:

```bash
cp -r deploy/argo-presync/overlays/example deploy/argo-presync/overlays/shop
# edit overlays/shop/kustomization.yaml → namespace: shop
kubectl apply -k deploy/argo-presync/overlays/shop
```

That produces `rehearsal-argo-presync-nodes-in-shop` (ClusterRole **and** Binding). A second overlay with `namespace: shop2` keeps both installs' node/pod access.

Provide ConfigMap `rehearsal-argo-presync-desired` with the proposed app YAML (mounted at `/gate/desired`).

## Cluster-wide pod list (capacity) — security trade-off

Default ClusterRole includes `pods` with verb **`list` only** (no `get`/`watch`) so scheduling-capacity can see cross-namespace pressure. A cluster-wide pod list returns **full pod specs**, including literal `env` values (credentials sometimes land there). This tool does not persist those values into snapshots (only name/namespace/phase/node/requests), but anyone who can run a pod as `rehearsal-argo-presync` in the app namespace can still list them via the API.

**Keep the default** if you want capacity checks for CPU/memory/slots. To disable: remove the `pods` rule from the ClusterRole (or omit `rbac-nodes.yaml` pods access). Without cluster-wide pods, scale / request increases fail closed as **UNKNOWN** (never a false PASS).

## AppProject `clusterResourceWhitelist`

ClusterRole / ClusterRoleBinding are **not** created by PreSync hooks (they are normal Application resources). Strict AppProjects must still allow them on the Application itself:

```yaml
clusterResourceWhitelist:
  - group: rbac.authorization.k8s.io
    kind: ClusterRole
  - group: rbac.authorization.k8s.io
    kind: ClusterRoleBinding
```

Each Application owns its own `…-in-<ns>` ClusterRole (not shared), so one app's prune/delete does not remove another's.

## Helm

```bash
helm template r deploy/helm/architecture-rehearsal \
  --set api.token=dev \
  --set argoPresync.enabled=true
```

Helm names ClusterRole and Binding with `-in-{{ .Release.Namespace }}`.

## Image

```bash
docker build -f Dockerfile.presync -t ghcr.io/justrunme/architecture-rehearsal-presync:1.8.0 .
```
