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

The PreSync Job must know the Application name (`--app` / `ARGOCD_APP_NAME`) so it can read these annotations (RBAC: `get`/`list` on `applications`).

## RBAC and live template compare

With `--live`, the gate dry-runs desired workloads the same way the sync will apply them:

| Sync mode | Dry-run |
| --------- | ------- |
| Argo default (client-side apply) | `kubectl apply --dry-run=server` (three-way merge via `last-applied-configuration`) |
| Argo `ServerSideApply=true` (`--server-side-apply` / `REHEARSAL_SERVER_SIDE_APPLY=true`) | SSA with field manager `argocd-controller` and `--force-conflicts` |

`kubectl apply --dry-run=server` issues a **patch** with `dryRun=All`. The shipped ClusterRole therefore grants `patch` on `deployments`, `statefulsets`, and `daemonsets` (in addition to get/list/watch). ReplicaSets stay get/list/watch only.

**Safety:** dry-run does not mutate cluster objects. On kind verification (`VERIFY-4`), Deployment `resourceVersion` and `generation` were unchanged after every `--live` run, and no `rehearsal` field manager appeared on the live object.

Without `last-applied-configuration` and without SSA mode, field removals cannot be predicted — the gate fails closed (treats the sync as a template rollout) unless a two-way equality check proves no change.

## Install

### Kustomize / raw manifests

See [`deploy/argo-presync/`](../deploy/argo-presync/). Provide ConfigMap `rehearsal-argo-presync-desired` and patch the ClusterRoleBinding subject namespace to the Application destination.

### Helm

```bash
helm template r deploy/helm/architecture-rehearsal \
  --set api.token=dev \
  --set argoPresync.enabled=true
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
