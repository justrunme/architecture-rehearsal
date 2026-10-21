# Argo CD PreSync change-impact gate

Kubernetes manifests that run `rehearsal argo-presync` as an Argo CD **PreSync** hook so risky GitOps changes are blocked inside the cluster before sync (not only in CI).

## Contents

| Path | Role |
| ---- | ---- |
| `base/` | ServiceAccount, Role/RoleBinding (hooks), per-ns ClusterRole/Binding (Nodes + Pods **list**), Job |
| `components/rbac-cluster/` | **Opt-in** Component: per-ns ClusterRole for namespaces/PVs + Applications |
| `overlays/example/` | Documented install: set `namespace:` + replacements → `…-in-<ns>` |

## Migration (1.8.0)

**Do not** `kubectl apply -k deploy/argo-presync` (root). That path now **fails at build** on purpose — the old root alias produced `…-nodes-in-default` with subject `default`, so a second namespace install stole the binding and Argo `destination.namespace` left the gate without Nodes/Pods access.

**Before:** Application/path pointed at `deploy/argo-presync` (or wrapped it with only `namespace:`).  
**After:** copy `overlays/example`, set `namespace:` to the Application destination, point Argo at that overlay directory.

```bash
cp -r deploy/argo-presync/overlays/example deploy/argo-presync/overlays/shop
# edit overlays/shop/kustomization.yaml → namespace: shop
kubectl apply -k deploy/argo-presync/overlays/shop
```

## Install (kustomize overlay)

```bash
cp -r deploy/argo-presync/overlays/example deploy/argo-presync/overlays/shop
# set namespace: shop
kubectl apply -k deploy/argo-presync/overlays/shop
```

That produces `rehearsal-argo-presync-nodes-in-shop` (ClusterRole **and** Binding). A second overlay with `namespace: shop2` keeps both installs' access.

Provide ConfigMap `rehearsal-argo-presync-desired` with the proposed app YAML (mounted at `/gate/desired`).

### Opt-in cluster RBAC (Component)

Do **not** add `- ../../base/rbac-cluster.yaml` (load restrictor rejects it). Use the Component:

```yaml
# in your overlay kustomization.yaml
components:
  - ../../components/rbac-cluster
```

Keep the `replacements` targets for `rehearsal-argo-presync-cluster-in-default` (see `overlays/example`). Building that overlay yields `…-cluster-in-<ns>` alongside nodes.

## Cluster-wide list (capacity) — security trade-off

Default ClusterRole grants **`list` only** on Nodes and Pods (no `get`/`watch`). A cluster-wide pod list returns **full pod specs**, including literal `env` values. This tool does not persist those values into snapshots (only name/namespace/phase/node/requests), but anyone who can run a pod as `rehearsal-argo-presync` can still list them via the API.

**Keep the default** for capacity checks. To disable: remove the `pods` rule. Without cluster-wide pods, scale / request / surge demand fails closed as **UNKNOWN**.

## AppProject `clusterResourceWhitelist`

```yaml
clusterResourceWhitelist:
  - group: rbac.authorization.k8s.io
    kind: ClusterRole
  - group: rbac.authorization.k8s.io
    kind: ClusterRoleBinding
```

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
