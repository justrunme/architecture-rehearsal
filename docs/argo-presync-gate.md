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

**Default RBAC** — install via an **overlay** that sets `namespace:` (see `deploy/argo-presync/overlays/example`). The root `deploy/argo-presync` path **fails to build** (migration: copy the example overlay).
- namespaced `Role` + `RoleBinding` as PreSync hooks
- per-namespace `ClusterRole` + `ClusterRoleBinding` (`…-nodes-in-<ns>`) — Nodes + Pods **list** only

**Opt-in cluster RBAC:** kustomize Component `components/rbac-cluster` (not a raw `base/rbac-cluster.yaml` path). Helm: `argoPresync.clusterRBAC=true`.

**Cluster-wide list trade-off:** `pods/list` (and `nodes/list`) return API objects; pod specs include literal env. Snapshots keep only name/namespace/phase/node/requests for foreign pods. Remove the pods rule to disable; capacity then **UNKNOWN** when slots/requests/surge are needed.

**Surge vs scale:** RollingUpdate surge is counted only when the pod template changes; plain replica scale counts only the delta (Recreate never uses surge).

**AppProject whitelist** (cluster-scoped objects are synced as normal resources, not hooks):

```yaml
clusterResourceWhitelist:
  - group: rbac.authorization.k8s.io
    kind: ClusterRole
  - group: rbac.authorization.k8s.io
    kind: ClusterRoleBinding
```

If Nodes or cluster-wide Pods cannot be read and the sync needs **new pod slots** (RollingUpdate surge on a template change, or a replica increase) or **higher requests**, `scheduling-capacity` returns **UNKNOWN** (never a false PASS/ALLOW). Env-only with `maxSurge: 0` and unchanged scheduling fields still **PASS** with a note when Nodes are absent. Unchanged affinity/requests on an env-only patch do not count as a scheduling-field change. Pods without requests count as **0** (scheduler semantics). Placement uses **per-node** free capacity on **eligible** nodes only (nodeSelector + required nodeAffinity + NoSchedule/NoExecute tolerations + not cordoned); a tainted control-plane with a roomy worker does not false-BLOCK. Pending pods (empty `nodeName`) are charged to the node with the most free CPU. New/surge pod requests **BLOCK** when they do not fit on eligible nodes. **UNKNOWN** for resources only when node allocatable CPU/memory is unreadable. A no-op sync (no template change, no replica/request increase) never **BLOCK**s on capacity.

Without `last-applied-configuration` in client mode, removals cannot be predicted. The gate fails closed with a distinct reason (`cannot predict removals: live object has no last-applied-configuration …`). If the rollout strategy itself is safe, `pdb-rollout` reports **UNKNOWN** (not a false “pods to 0” BLOCK).

`strategy.type: Recreate` is treated as availability 0: template change → BLOCK; unchanged template (or Recreate-only switch) → WARN, with message `Recreate strategy terminates all pods before starting new ones; availability drops to 0 during rollout`.

## Install

### Kustomize / raw manifests

See [`deploy/argo-presync/`](../deploy/argo-presync/) and `overlays/example`: copy the overlay, set `namespace:` to the Application destination, apply with `kubectl apply -k`. Provide ConfigMap `rehearsal-argo-presync-desired`. Optionally enable Component `components/rbac-cluster` in the overlay.

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
