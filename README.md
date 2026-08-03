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

**Status: v0.3.0** — strong deterministic prototype with real offline K8s graph pipeline.  
**Not** production-grade multi-cluster platform. **Not** a published GHCR product yet (Dockerfile is reference until CI publishes images).

---

## Capability matrix (honest)

| Area | Status |
| ---- | ------ |
| Snapshot graph + validation | **Supported** |
| Deep-copy ApplyChange (baseline immutable) | **Supported** |
| Golden scenarios (fixtures with hand-built edges) | **Supported** |
| Scenario prerequisites → `unknown` | **Supported** |
| Offline K8s YAML collector (List, recursive, edges) | **Supported** (v0.3) |
| Manifest change compiler with **namespace scope** | **Supported** (removals off by default) |
| Terraform plan → change | **Reference / experimental** |
| `rehearsal verify` | **Reference** (needs `meta.observed_failures` / Pending markers) |
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
make demo
```

```bash
make build
./bin/rehearsal analyze \
  --baseline examples/golden/rwo-node-loss/baseline.json \
  --change examples/golden/rwo-node-loss/change.json \
  --html out/rwo-report.html
```

### Real YAML → graph (v0.3)

```bash
# dump (example)
kubectl get node,deploy,sts,ds,po,svc,pvc,pv,pdb,hpa,sa,ing -A -o yaml > /tmp/dump.yaml
mkdir -p /tmp/k8s && cp /tmp/dump.yaml /tmp/k8s/

./bin/rehearsal snapshot k8s --dir /tmp/k8s --cluster acme-prod --out baseline.json

# scoped change (never mass-deletes out-of-scope workloads)
./bin/rehearsal change manifests \
  --baseline baseline.json \
  --dir ./rendered-chart \
  --namespace payments \
  --out change.json
# add --allow-remove only when rendered dir is complete for that scope
```

### Exit codes

| Code | Meaning |
| ---: | ------- |
| 0 | approve |
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

**Is:** offline change-gate core you can run in CI with fixtures or rendered YAML.  
**Is not yet:** signed immutable evidence store, published multi-arch image, live multi-cluster control plane.

Version discipline: inflated `v1`/`v2` tags were retracted; current line is **0.3.x**.

---

## License

Apache-2.0
