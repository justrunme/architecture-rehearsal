# Argo CD impact gate fixtures

Asserted scenarios for `rehearsal argo-impact`:

| Scenario | Expected gate | Exit |
| -------- | ------------- | ---: |
| `unschedulable/` | BLOCK (replicas vs capacity) | 3 |
| `netpol-cut/` | BLOCK (NetworkPolicy cuts app→postgres) | 3 |
| `pdb-violation/` | BLOCK (PDB vs rollout) | 3 |
| `unknown-pdb-inventory/` | UNKNOWN (PDB inventory not collected) | 4 |
| `benign/` | PASS | 0 |
| `c5-rollout-maxunavail/` | BLOCK (`maxUnavailable: 100%` / `maxSurge: 0`; PDB does not protect rollouts) | 3 |
| `fp3-maxunavail-one/` | PASS (`maxUnavailable: 1` control) | 0 |
| `fp4-zero-avail-no-pdb/` | WARN (dangerous strategy already live, template unchanged) | 1 |
| `rollout-template-live/` | Kind `--live`: identical desired → WARN/PASS (not BLOCK) | — |
| `rollout-template-live-env/` | Kind `--live`: env change → BLOCK | — |

```bash
bash scripts/e2e_argo_impact.sh
# Kind + Argo CD:
bash scripts/kind-argo-impact-e2e.sh
# Kind live template compare (server-side dry-run):
bash scripts/kind-rollout-template-e2e.sh
```
