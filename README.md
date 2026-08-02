# Architecture Rehearsal

**Know what breaks before you deploy.**

Self-hosted **change gate** for Kubernetes and cloud infrastructure:

```text
IaC / Kubernetes change
  → architecture graph
  → deterministic impact analysis
  → CI decision (approve | warn | block | unknown)
  → deploy outside Rehearsal
  → post-deploy verification
  → immutable evidence (SHA-256)
```

> **Graph and rules decide. AI only explains** (optional, not in the risk path).  
> **Missing data never becomes a false approve.**

[![CI](https://github.com/justrunme/architecture-rehearsal/actions/workflows/ci.yml/badge.svg)](https://github.com/justrunme/architecture-rehearsal/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/justrunme/architecture-rehearsal)](https://github.com/justrunme/architecture-rehearsal/releases)
[![License](https://img.shields.io/badge/license-Apache%202.0-blue.svg)](LICENSE)

**Status: v1.0 / v2.0 multi-cluster merge** — production-lean CLI & container for GitHub Actions, GitLab CI, GitOps pipelines. Not a SaaS. Not a Kubernetes operator. Not an AI that manages clusters.

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

### Exit codes (CI)

| Code | Meaning |
| ---: | ------- |
| 0 | approve / verified |
| 1 | warn / diverged |
| 2 | usage or validation error |
| 3 | block |
| 4 | **unknown** (insufficient evidence) |
| 5 | internal error |

Treat **3 and 4 as gate failures** unless you explicitly allow them.

---

## What is supported

| Area | Support |
| ---- | ------- |
| Snapshot graph + change envelope | **Supported** |
| Deterministic scenarios (RWO, CNI, Prom, PDB, routing, volume AZ) | **Supported** |
| Evidence bundle + SHA-256 | **Supported** |
| `rehearsal verify` closed loop | **Supported** |
| Manifest / Terraform plan → change | **Supported** (offline) |
| K8s YAML directory → snapshot | **Supported** (read-only, no Secret values) |
| Multi-cluster merge | **Supported** (v2 API `MergeSnapshots`) |
| Live kubeconfig collector | Reference (use `kubectl get -o yaml` + `snapshot k8s`) |
| Web UI / LLM / auto-deploy | **Not supported** |

---

## Golden scenarios

1. **RWO + node loss** — stateful PVC cannot reattach; dependents cascade  
2. **CNI / IP capacity** — derived from baseline→proposed replicas + maxSurge  
3. **Prometheus zero-match** — metric-specific label schema; valid PromQL, 0 series  
4. **PDB disruption** — minAvailable violated by node loss  
5. **Service routing** — backends all removed  
6. **Volume AZ** — PVC zone has no remaining nodes  

---

## CLI

```bash
rehearsal analyze  --baseline FILE --change FILE [--out DIR] [--html PATH] [--quiet]
rehearsal verify   --report report.json --observed post.json
rehearsal snapshot k8s --dir manifests/ --cluster acme-prod --out baseline.json
rehearsal change   manifests --baseline baseline.json --dir rendered/ --out change.json
rehearsal change   terraform --plan plan.json --out change.json
rehearsal version
```

Container:

```bash
docker run --rm -v "$PWD:/work" -w /work \
  ghcr.io/justrunme/architecture-rehearsal:1.0.0 \
  analyze --baseline baseline.json --change change.json --out evidence --quiet
```

---

## Architecture

```text
baseline snapshot (deployed + observed facts)
change envelope (desired / plan / diff)
        │
        ▼
  validate (fail-closed)
  ApplyChange (deep-copy — baseline immutable)
        │
        ▼
  scenario engine (rules only)
        │
        ▼
  report + decision + semantic digest
  evidence-manifest.json (sha256 per file)
        │
        ▼
  [external deploy]
        │
        ▼
  verify(prediction, observed snapshot)
  → verified | diverged | inconclusive
```

### Multi-cluster (v2)

```go
merged := graph.MergeSnapshots("fleet", clusterA, clusterB)
```

Node IDs become `clusterId::nodeId` so repositories/clusters can be analyzed together without collisions.

---

## Production definition

- Baseline immutable under `ApplyChange`  
- Invalid graphs rejected (duplicate IDs, dangling edges)  
- Incomplete coverage → `unknown`, never silent approve  
- Evidence writes are fail-closed with hashes  
- Non-root distroless image  
- No Secret value collection  

See [docs/security.md](docs/security.md), [docs/upgrade.md](docs/upgrade.md), [docs/adr/0001-decision-model.md](docs/adr/0001-decision-model.md).

---

## License

Apache-2.0
