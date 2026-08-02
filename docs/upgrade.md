# Upgrade guide

## 0.1.x → 1.0.0

Breaking:

- `rollback_available` (bool) → `rollback` (`available|unavailable|unknown`)
- Exit code `4` = `unknown` (insufficient evidence)
- `analyze.Run` returns `(*Report, error)`
- Fixtures renamed to `acme-prod` / `gitops` (no internal cluster names)
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
