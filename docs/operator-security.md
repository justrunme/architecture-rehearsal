# Operator security model (v1.5.1+)

## Threat: token exfiltration via CR

**Before v1.5.1**, a user who could create `RehearsalRun` could set `spec.controlPlaneURL`  
to an attacker-controlled host. The operator would send `REHEARSAL_API_TOKEN` there.

**Mitigation:**

1. Field removed from API type and CRD (no Spec field for API URL; typed client ignores unknowns).
2. Operator reads URL only from `REHEARSAL_API_URL` (Deployment env).
3. Token only from Secret (`secretKeyRef`).
4. NetworkPolicy restricts egress to control-plane Service + DNS (+ API for leader election).

## Least privilege

| Component | Permission |
| --------- | ---------- |
| Operator SA | CRD get/list/watch/update status only (+ leases for leader election) |
| Token Secret | Mounted only into operator pods |
| Control plane token | Prefer org-scoped operator role, not platform-admin |

## Deployment hardening

- `runAsNonRoot`
- `readOnlyRootFilesystem`
- drop all capabilities
- distroless image (`Dockerfile.operator`)
- leader election for HA (2 replicas default in `config/operator`)

## What not to do

- Do not re-add API URL fields on the CR.
- Do not put the API token in CR annotations/labels/env from ConfigMaps owned by users.
- Do not run the operator as cluster-admin.
