# Operational runbooks

**Target version:** v1.5.3

## Deploy control plane (+ optional operator)

```bash
export REHEARSAL_API_TOKEN="$(openssl rand -hex 32)"

helm upgrade --install rehearsal deploy/helm/architecture-rehearsal \
  --set api.token="$REHEARSAL_API_TOKEN" \
  --set image.tag=1.5.3 \
  --set operator.enabled=true \
  --set operator.image.tag=1.5.3

kubectl get crd rehearsalruns.rehearsal.io
kubectl rollout status deploy/architecture-rehearsal
kubectl rollout status deploy/architecture-rehearsal-operator
```

Without operator: omit `operator.enabled` (default `false`).

## Offline gate in CI

```bash
rehearsal snapshot k8s --dir dump --out baseline.json
rehearsal change manifests --baseline baseline.json --dir rendered --namespace app --out change.json
rehearsal analyze --baseline baseline.json --change change.json --out out --quiet
# exit 0 = approve · 1 = warn · 3 = block · 4 = unknown
```

## Verify after deploy

```bash
rehearsal snapshot k8s --dir observed-dump --phase observed --meta meta.json --out observed.json
rehearsal verify --report out/latest-report.json --observed observed.json \
  --baseline baseline.json --change change.json
```

## Backup / restore

| Backend | Action |
| ------- | ------ |
| SQLite | Copy DB file + blob directory; or `rehearsal backup --db … --out …` |
| PostgreSQL | Standard `pg_dump` / restore |
| Blobs | Copy content-addressed blob root (or versioned bucket) |

After restore, re-verify digests before trusting cached decisions.

## Disaster recovery

1. Restore store + evidence blobs.
2. Re-verify digests: `rehearsal evidence verify-chain`.
3. Re-issue gate decisions only from re-computed analyze — never trust unsigned cache alone.

## Health checks

```bash
curl -sf http://127.0.0.1:8080/healthz
curl -sf http://127.0.0.1:8080/readyz
curl -sf -H "Authorization: Bearer $REHEARSAL_API_TOKEN" http://127.0.0.1:8080/v1/metrics
```

## Related

- [../operator.md](../operator.md)  
- [../api.md](../api.md)  
- [../slo.md](../slo.md)  
