# Architecture Rehearsal

**Know what breaks before you deploy.**

Self-hosted **change impact analysis** for Kubernetes / IaC pipelines:

```text
YAML / plan → architecture graph → deterministic scenarios → approve|warn|block|unknown → evidence → verify
```

> **Graph and rules decide. AI only explains** (optional — not implemented in risk path).  
> **Missing data never becomes a false approve.**

[![CI](https://github.com/justrunme/architecture-rehearsal/actions/workflows/ci.yml/badge.svg)](https://github.com/justrunme/architecture-rehearsal/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/justrunme/architecture-rehearsal)](https://github.com/justrunme/architecture-rehearsal/releases)
[![License](https://img.shields.io/badge/license-Apache%202.0-blue.svg)](LICENSE)

**Status: v0.7.0** — deterministic engineering prototype with fail-closed offline gate, independent verify, live kubectl collect, and local platform primitives (run store / signed evidence / multi-cluster merge).  
**Not** a multi-tenant SaaS. **Not** a published GHCR multi-arch product yet.

---

## Capability matrix (honest)

| Area | Status |
| ---- | ------ |
| Snapshot graph + validation | **Supported** |
| Deep-copy ApplyChange (baseline immutable) | **Supported** |
| Golden scenarios (fixtures with hand-built edges) | **Supported** |
| Scenario prerequisites → `unknown` | **Supported** |
| Offline K8s YAML collector (List, recursive, edges) | **Supported** |
| Fail-closed YAML (strict default, `--allow-partial`) | **Supported** (v0.4.1+) |
| Manifest change compiler with **namespace scope** | **Supported** (removals off by default) |
| E2E path: List dump → graph → scoped change → analyze → verify | **Supported** |
| Independent verify (Pending pods, change identity, delta) | **Supported** (v0.5+) |
| Live read-only collect (`kubectl`) | **Supported** (v0.6; requires kubectl) |
| ownerReferences ownership chain | **Supported** (v0.6) |
| Capacity: scheduling estimate vs explicit CNI meta | **Supported** (estimate default; real CNI needs `cni_ip_available`) |
| Run store + audit trail (filesystem) | **Supported** (v0.7 local) |
| HMAC-signed evidence | **Supported** (v0.7; not Sigstore) |
| Multi-cluster merge CLI | **Supported** (v0.7) |
| Config RBAC model | **Supported** (v0.7 local policy) |
| Terraform plan → change | **Reference / experimental** |
| Distroless Dockerfile | **Reference** (not published by CI yet) |
| GH/GL integration examples | **Reference** |
| Live client-go watch / operator | **Not supported** |
| Web UI / LLM / auto-deploy | **Not supported** |
| Network authn / multi-tenant control plane | **Not supported** |

---

## Quick start

```bash
git clone https://github.com/justrunme/architecture-rehearsal.git
cd architecture-rehearsal
make demo    # golden scenarios (expect block)
make e2e     # full dump → graph → scoped change → verify path
```

```bash
make build
./bin/rehearsal version   # 0.7.0
```

### Offline iron path

```bash
./bin/rehearsal snapshot k8s --dir examples/e2e-pipeline/cluster-dump --out baseline.json
./bin/rehearsal change manifests \
  --baseline baseline.json \
  --dir examples/e2e-pipeline/rendered-chart \
  --namespace payments \
  --out change.json
./bin/rehearsal analyze --baseline baseline.json --change change.json --out out --store out/runs --quiet
# exit 3 = block

./bin/rehearsal snapshot k8s \
  --dir examples/e2e-pipeline/observed-dump \
  --phase observed \
  --meta examples/e2e-pipeline/observed-meta.json \
  --out observed.json
./bin/rehearsal verify \
  --report out/latest-report.json \
  --observed observed.json \
  --baseline baseline.json \
  --change change.json
```

### Live collect (read-only)

```bash
./bin/rehearsal snapshot k8s --live --cluster acme-prod --out baseline.json
# optional: --kubeconfig ~/.kube/config --context prod
```

### Exit codes

| Code | Meaning |
| ---: | ------- |
| 0 | approve / verified |
| 1 | warn / verify diverged |
| 2 | validation / usage error |
| 3 | block |
| 4 | **unknown** (insufficient evidence) |
| 5 | internal error |

---

## Scenarios

Each scenario declares prerequisites. Missing data → **unknown**, not silent approve.

1. **RWO + node loss** — needs PVC + volume edges  
2. **CNI / scheduling capacity** — replica delta + surge; uses scheduling estimate unless `meta.cni_ip_available` is injected  
3. **Prometheus zero-match** — metric-specific `meta.metric_labels`  
4. **PDB disruption** — needs PROTECTED_BY edges; supports percentage minAvailable  
5. **Service routing** — needs ROUTES_TO edges  
6. **Volume AZ** — needs PVC zone + remaining nodes  

---

## What this is (and is not)

**Is:** offline (+ optional live kubectl) change-gate core you can run in CI, with independent verify and local run persistence.  
**Is not yet:** signed immutable multi-party evidence store (Sigstore), published multi-arch image, multi-tenant control plane.

Version line: **0.7.x** after honest 0.1–0.4 rebuild (inflated v1/v2 tags were retracted).

---

## License

Apache-2.0
