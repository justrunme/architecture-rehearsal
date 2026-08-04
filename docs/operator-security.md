# Operator security model

**Since:** v1.5.1 · **Current:** v1.5.3

## Threat: token exfiltration via CR

```mermaid
flowchart TB
  subgraph Before["Before v1.5.1 — broken"]
    A1[Attacker creates CR] -->|spec.controlPlaneURL = evil.host| OP1[Operator]
    OP1 -->|sends REHEARSAL_API_TOKEN| Evil[Attacker host]
  end

  subgraph After["v1.5.1+ — fixed"]
    A2[Attacker creates CR] -->|baseline / change only| OP2[Operator]
    Deploy[Deployment env] -->|REHEARSAL_API_URL| OP2
    Sec[Secret] -->|REHEARSAL_API_TOKEN| OP2
    OP2 --> CP[Real control plane only]
  end

  style Before fill:#fff5f5,stroke:#c33
  style After fill:#f5fff5,stroke:#3a3
  style Evil fill:#fcc,stroke:#900
```

**Mitigation:**

1. Field removed from API type and CRD (no Spec field for API URL; typed client ignores unknown keys).
2. Operator reads URL only from `REHEARSAL_API_URL` (Deployment env).
3. Token only from Secret (`secretKeyRef`).
4. NetworkPolicy (optional) restricts egress to control-plane Service + DNS + **explicit** API CIDRs — never `0.0.0.0/0`.

## Least privilege

| Component | Permission |
| --------- | ---------- |
| Operator SA | `get/list/watch/update/patch` on `rehearsalruns` + status/finalizers |
| Operator SA | **No** create/delete of user CRs |
| Operator SA | leases (leader election) + events |
| Token Secret | Mounted only into operator pods |
| Control plane token | Prefer org-scoped operator role, not platform-admin |

## Deployment hardening

- `runAsNonRoot` / `runAsUser: 65532` (distroless nonroot)
- `readOnlyRootFilesystem` + emptyDir `/tmp`
- Drop all capabilities
- Distroless image (`Dockerfile.operator`)
- Leader election for HA (chart default 2 replicas)

## What not to do

- Do not re-add API URL fields on the CR.
- Do not put the API token in CR annotations, labels, or user-owned ConfigMaps.
- Do not run the operator as cluster-admin.
- Do not enable NetworkPolicy with open egress (`0.0.0.0/0`).
