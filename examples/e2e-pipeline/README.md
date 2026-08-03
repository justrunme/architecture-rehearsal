# E2E pipeline fixture (v0.4)

One iron path — no hand-built golden graph:

```text
kubectl List dump → snapshot k8s → scoped change manifests → analyze → verify
```

| Path | Role |
| ---- | ---- |
| `cluster-dump/` | Offline kubectl List of acme-prod (tight pod capacity) |
| `rendered-chart/` | Helm-rendered payments scale-up (desired) |
| `observed-dump/` | Post-deploy dump after the change was applied anyway |
| `observed-meta.json` | Operator/CI annotations (`observed_failures`) for verify |

Run:

```bash
make e2e
# or
bash scripts/e2e_pipeline.sh
```

Expected: **analyze exit 3 (block)** on CNI capacity, then **verify exit 0 (verified)** against observed evidence.
