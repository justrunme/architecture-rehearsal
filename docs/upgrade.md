# Upgrade guide

## Tag policy

Published tags (`v1.5.1`, `v1.5.2`, `v1.5.3`, …) are **immutable**.  
Ship fixes as a new version; do not move tags.

## 1.5.2 → 1.5.3

- Prefer multi-arch images: `ghcr.io/justrunme/architecture-rehearsal:1.5.3` (+ `-operator`)
- Helm chart `appVersion` / image tags → `1.5.3`
- Empty `PolicyPath` no longer breaks gate when workdir is set (engine path fix)
- Optional chart `extraVolumes` / `extraVolumeMounts` for fixture mounts

## 1.5.1 → 1.5.2

- Helm installs CRD from `crds/`
- NetworkPolicy **disabled by default**
- Run id includes UID: `{ns}-{name}-{uid8}-g{gen}`
- Spec match on all EnsureRun results (including sandboxed paths)

## 1.5.0 → 1.5.1

- **Breaking for CR users:** `spec.controlPlaneURL` removed  
- Set `REHEARSAL_API_URL` only on the operator Deployment

## 0.1.x → 1.0.0

Breaking:

- `rollback_available` (bool) → `rollback` (`available|unavailable|unknown`)
- Exit code `4` = `unknown` (insufficient evidence)
- `analyze.Run` returns `(*Report, error)`
- Fixtures renamed to `acme-prod` / `gitops`
- Prometheus `metric_labels` should be **metric-specific** maps

Compatible:

- Change envelope `kind` still means change type
- Snapshot nodes/edges shape unchanged

## Contracts

Documents may include:

```json
"apiVersion": "rehearsal.io/v1alpha1"
```

Validation is fail-closed on duplicate IDs and dangling edges.
