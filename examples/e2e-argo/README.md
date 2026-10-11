# Argo CD impact gate fixtures

Four asserted scenarios for `rehearsal argo-impact`:

| Scenario | Expected gate | Exit |
| -------- | ------------- | ---: |
| `unschedulable/` | BLOCK (replicas vs capacity) | 3 |
| `netpol-cut/` | BLOCK (NetworkPolicy cuts app→postgres) | 3 |
| `pdb-violation/` | WARN or BLOCK (PDB vs rollout) | 1 or 3 |
| `benign/` | PASS | 0 |

```bash
bash scripts/e2e_argo_impact.sh
# Kind + Argo CD:
bash scripts/kind-argo-impact-e2e.sh
```
