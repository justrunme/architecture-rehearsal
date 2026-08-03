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

**Status: v0.4.0** — strong deterministic prototype with a full offline E2E path (dump → graph → scoped change → gate → verify).  
**Not** production-grade multi-cluster platform. **Not** a published GHCR product yet (Dockerfile is reference until CI publishes images).

---

## Capability matrix (honest)

| Area | Status |
| ---- | ------ |
| Snapshot graph + validation | **Supported** |
| Deep-copy ApplyChange (baseline immutable) | **Supported** |
| Golden scenarios (fixtures with hand-built edges) | **Supported** |
| Scenario prerequisites → `unknown` | **Supported** |
| Offline K8s YAML collector (List, recursive, edges) | **Supported** |
| Manifest change compiler with **namespace scope** | **Supported** (removals off by default) |
| **E2E path**: List dump → graph → scoped change → analyze → verify | **Supported** (v0.4) |
| Terraform plan → change | **Reference / experimental** |
| `rehearsal verify` | **Supported** with `meta.observed_failures` / Pending markers |
| Distroless Dockerfile | **Reference** (not published by CI yet) |
| GH/GL integration examples | **Reference** |
| Multi-cluster merge | **Experimental** (`internal` only; no CLI) |
| Live kubeconfig collector | **Not supported** |
| Web UI / LLM / auto-deploy / operator | **Not supported** |

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
./bin/rehearsal analyze \
  --baseline examples/golden/rwo-node-loss/baseline.json \
  --change examples/golden/rwo-node-loss/change.json \
  --html out/rwo-report.html
```

### Real YAML → graph → scoped change → verify (v0.4)

```bash
# 1) dump from a cluster (or use examples/e2e-pipeline/cluster-dump)
kubectl get node,ns,deploy,sts,ds,po,svc,pvc,pv,pdb,hpa,sa,ing -A -o yaml > /tmp/dump.yaml
mkdir -p /tmp/k8s && cp /tmp/dump.yaml /tmp/k8s/

./bin/rehearsal snapshot k8s --dir /tmp/k8s --cluster acme-prod --out baseline.json

# 2) scoped change from helm-rendered manifests (never mass-deletes out-of-scope)
./bin/rehearsal change manifests \
  --baseline baseline.json \
  --dir ./rendered-chart \
  --namespace payments \
  --out change.json
# add --allow-remove only when rendered dir is complete for that scope

# 3) gate
./bin/rehearsal analyze --baseline baseline.json --change change.json --out out --quiet
# exit 3 = block

# 4) post-deploy: dump again + optional operator meta for verify
./bin/rehearsal snapshot k8s \
  --dir /tmp/observed \
  --cluster acme-prod \
  --phase observed \
  --meta observed-meta.json \
  --out observed.json

./bin/rehearsal verify --report out/latest-report.json --observed observed.json
```

Fixture walkthrough (no live cluster): `examples/e2e-pipeline/` + `make e2e`.

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
2. **CNI capacity** — replica delta + surge from baseline/proposed; needs capacity meta or node allocatablePods  
3. **Prometheus zero-match** — metric-specific `meta.metric_labels`  
4. **PDB disruption** — needs PROTECTED_BY edges  
5. **Service routing** — needs ROUTES_TO edges  
6. **Volume AZ** — needs PVC zone + remaining nodes  

---

## What this is (and is not)

**Is:** offline change-gate core you can run in CI with fixtures or rendered YAML, including a full dump→verify path.  
**Is not yet:** signed immutable evidence store, published multi-arch image, live multi-cluster control plane.

Version discipline: inflated `v1`/`v2` tags were retracted; current line is **0.4.x**.

---

## License

Apache-2.0
