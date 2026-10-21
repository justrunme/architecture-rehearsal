# Argo CD PreSync change-impact gate

**Who:** platform teams running Argo CD GitOps.  
**Problem:** CI change-impact checks can be bypassed (manual sync, direct commit to the tracked branch, UI sync). A PreSync hook runs on **every** sync inside the cluster.  
**Metric (fixtures):** % of risky syncs blocked, % of safe syncs falsely blocked, and PreSync hook latency added to sync — see [EVIDENCE.md](EVIDENCE.md).

This wraps the native gate from [`argo-impact-gate.md`](argo-impact-gate.md) as an Argo CD **PreSync** Job.

```text
Application sync
  → PreSync Job (rehearsal argo-presync)
       desired ConfigMap + live cluster snapshot
       → impact detectors → JSON decision line
  → exit 0 (ALLOW/WARN) → sync continues
  → exit ≠ 0 (BLOCK/UNKNOWN) → Argo aborts; live objects unchanged
```

## Hook contract

| Decision | Job exit | Sync |
| -------- | -------: | ---- |
| `ALLOW`  | 0 | proceeds |
| `WARN`   | 0 | proceeds |
| `BLOCK`  | 3 | aborted |
| `UNKNOWN`| 4 | aborted (fail closed) |

Stdout: **one JSON line** with `decision`, `reasons`, `ruleIds`, `durationMs` (and optional `override`).

Fail closed on missing inputs, API errors, timeouts, or unknown decisions.

## Break-glass

Annotate the Application (audited; reason required):

```yaml
metadata:
  annotations:
    rehearsal.io/break-glass: "true"
    rehearsal.io/break-glass-reason: "INC-123 emergency rollback"
```

The Job logs `BREAK-GLASS OVERRIDE reason=...` and emits `override` in the JSON line. Missing reason → fail closed.

The PreSync Job must know the Application name (`--app` / `ARGOCD_APP_NAME`) so it can read these annotations (RBAC: `get`/`list` on `applications` via optional cluster RBAC).

## RBAC and live template compare

With `--live`, the gate dry-runs desired workloads the same way the sync will apply them:

| Sync mode | Dry-run |
| --------- | ------- |
| Auto (default) | SSA when live `managedFields` include manager `argocd-controller` with operation `Apply`; otherwise client-side three-way `kubectl apply --dry-run=server` |
| Force SSA (`--server-side-apply` / `REHEARSAL_SERVER_SIDE_APPLY=true`) | SSA with field manager `argocd-controller` and `--force-conflicts` |
| Force client (`REHEARSAL_SERVER_SIDE_APPLY=false`) | Client-side three-way even if Argo SSA managers are present |

`kubectl apply --dry-run=server` issues a **patch** with `dryRun=All`. The shipped Role therefore grants `patch` on `deployments`, `statefulsets`, and `daemonsets` in the Application destination namespace. Dry-run does **not** mutate cluster objects: on kind verification, Deployment `resourceVersion` / `generation` were unchanged after every `--live` run.

**Change scope vs live collect:** `--namespace` scopes the change envelope only (it does **not** narrow inventory). Live namespaced inventory is limited only by `--live-namespace` / `REHEARSAL_LIVE_NAMESPACE` (PreSync sets this to the Job namespace). **Nodes and Pods are always fetched cluster-wide** (best-effort) so scheduling-capacity sees cross-namespace pressure. kubectl get for dry-run compare uses `--show-managed-fields` so Argo SSA (`argocd-controller` Apply) is auto-detected.

**Default RBAC** — install via an **overlay** that sets `namespace:` (see `deploy/argo-presync/overlays/example`); do not rely on editing only the base:
- namespaced `Role` + `RoleBinding` as PreSync hooks (`rbac.yaml`)
- per-namespace `ClusterRole` + `ClusterRoleBinding` (`…-nodes-in-<ns>`) as normal resources (`rbac-nodes.yaml`) — Nodes get/list/watch, Pods **list** only

**Opt-in cluster RBAC** (`rbac-cluster.yaml`, Helm `argoPresync.clusterRBAC=true`): Namespaces / PVs and Argo `applications`; also per-namespace `…-cluster-in-<ns>`.

**Cluster-wide pod list trade-off:** `pods/list` returns full specs (including literal env). Snapshots keep only name/namespace/phase/node/requests for foreign pods, but the SA can still list specs via the API. Remove the pods rule to disable; capacity then fails closed as **UNKNOWN** when slots/requests are needed.

**AppProject whitelist** (cluster-scoped objects are synced as normal resources, not hooks):

```yaml
clusterResourceWhitelist:
  - group: rbac.authorization.k8s.io
    kind: ClusterRole
  - group: rbac.authorization.k8s.io
    kind: ClusterRoleBinding
```

If Nodes cannot be read and the sync does **not** *change* scheduling-relevant fields versus live (nodeSelector, affinity, tolerations, topologySpreadConstraints, resource requests, or replica increases), `scheduling-capacity` returns **PASS** with a note; if it does change them, **UNKNOWN** (fail closed). Unchanged affinity/requests on an env-only patch do not count. If cluster-wide Pods cannot be listed and the change needs new pod slots or higher requests, the detector returns **UNKNOWN** (never a false PASS on incomplete occupancy). Pods without requests count as **0** (scheduler semantics); new/surge pod requests are subtracted from free allocatable CPU/memory and **BLOCK** when they do not fit. **UNKNOWN** for resources only when node allocatable CPU/memory is unreadable.

Without `last-applied-configuration` in client mode, removals cannot be predicted. The gate fails closed with a distinct reason (`cannot predict removals: live object has no last-applied-configuration …`). If the rollout strategy itself is safe, `pdb-rollout` reports **UNKNOWN** (not a false “pods to 0” BLOCK).

`strategy.type: Recreate` is treated as availability 0: template change → BLOCK; unchanged template (or Recreate-only switch) → WARN, with message `Recreate strategy terminates all pods before starting new ones; availability drops to 0 during rollout`.

## Install

### Kustomize / raw manifests

See [`deploy/argo-presync/`](../deploy/argo-presync/). Provide ConfigMap `rehearsal-argo-presync-desired` and set the RoleBinding / ServiceAccount namespace to the Application destination. Optionally include `rbac-cluster.yaml`.

### Helm

```bash
helm template r deploy/helm/architecture-rehearsal \
  --set api.token=dev \
  --set argoPresync.enabled=true
# Optional cluster reads / break-glass Applications:
# --set argoPresync.clusterRBAC=true
```

### Image

```bash
docker build -f Dockerfile.presync -t ghcr.io/justrunme/architecture-rehearsal-presync:1.8.0 .
```

Binary entrypoint:

```bash
rehearsal argo-presync --desired-dir /gate/desired --live --out /tmp/argo-presync
```

## CI

Kind e2e (pinned Argo CD): `bash scripts/kind-argo-presync-e2e.sh` — job `argo-presync-kind-e2e`.

Scenarios:

1. Safe change → `Synced` + `Healthy`
2. Risky change → operation `Failed`/`Error`; live Deployment unchanged
3. Gate failure (bad desired path) → fail closed
4. Break-glass → sync applies; override in Job logs
