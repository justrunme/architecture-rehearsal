# Operational runbooks

## Deploy control plane

```bash
helm upgrade --install rehearsal deploy/helm/architecture-rehearsal \
  --set image.tag=1.0.0 \
  --set api.token=$(openssl rand -hex 16)
```

## Offline gate in CI

```bash
rehearsal snapshot k8s --dir dump --out baseline.json
rehearsal change manifests --baseline baseline.json --dir rendered --namespace app --out change.json
rehearsal analyze --baseline baseline.json --change change.json --out out --quiet
# exit 3 = block
```

## Verify after deploy

```bash
rehearsal snapshot k8s --dir observed-dump --phase observed --meta meta.json --out observed.json
rehearsal verify --report out/latest-report.json --observed observed.json \
  --baseline baseline.json --change change.json
```

## Backup / restore

- Export run store directory (`out/runs`) and evidence bundles.
- PostgreSQL path: use standard pg_dump when enabled.
- Object storage: versioned bucket with retention policy CRD intent.

## Disaster recovery

1. Restore store + evidence blobs.
2. Re-verify digests with `rehearsal evidence verify-chain`.
3. Re-issue gate decisions only from re-computed analyze (never trust unsigned cache alone).
