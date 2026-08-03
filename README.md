# Architecture Rehearsal

**Know what breaks before you deploy — and prove whether you were right.**

Self-hosted **deterministic pre-deployment failure simulation** and **post-deployment verification control plane**:

```text
Collect → Model → Propose → Rehearse → Gate → Observe → Verify → Calibrate
```

> **Graph and rules decide. AI only explains** (optional — not in risk path).  
> **Missing data never becomes a false approve.**  
> **Every production gate decision links to a content-addressed evidence chain.**

[![CI](https://github.com/justrunme/architecture-rehearsal/actions/workflows/ci.yml/badge.svg)](https://github.com/justrunme/architecture-rehearsal/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/justrunme/architecture-rehearsal)](https://github.com/justrunme/architecture-rehearsal/releases)
[![License](https://img.shields.io/badge/license-Apache%202.0-blue.svg)](LICENSE)

**Status: v1.0.0** — stable production **contract** for offline + self-hosted control plane.  
Still not a multi-tenant SaaS; still not a published multi-arch GHCR product until your registry publish pipeline runs.

---

## Product positioning

> Architecture Rehearsal is a deterministic pre-deployment failure simulation and post-deployment verification control plane. It builds a causal architecture graph, predicts change impact, blocks unsafe deployments, verifies observed outcomes, and continuously measures the accuracy of its own scenarios.

---

## Capability matrix (honest)

| Area | Status |
| ---- | ------ |
| Offline K8s graph collect + edges | **Supported** |
| Fail-closed YAML | **Supported** |
| Causal verify (no global Pending free-pass) | **Supported** (v0.7.2+) |
| Evidence digests + chain | **Supported** (v0.8+) |
| DSSE-style sign (HMAC / Ed25519) | **Supported** (not full Sigstore) |
| RehearsalRun lifecycle + operator loop | **Supported** (offline JSON CRDs) |
| GitOps policy gate | **Supported** (policy engine + GH reference) |
| Control plane HTTP API | **Supported** (`rehearsal serve`) |
| Hierarchical RBAC + bearer auth | **Supported** (local; OIDC stub) |
| Calibration engine | **Supported** |
| Scenario package registry / SDK surface | **Supported** |
| Helm chart | **Supported** (`deploy/helm`) |
| Live kubectl collect | **Supported** |
| Live controller-runtime operator | **Reference** (JSON reconciler) |
| PostgreSQL + S3 backends | **Interface / docs** (memory default) |
| Multi-tenant SaaS | **Not supported** |
| LLM in risk path | **Not supported** |

---

## Quick start

```bash
git clone https://github.com/justrunme/architecture-rehearsal.git
cd architecture-rehearsal
make demo && make e2e
make build && ./bin/rehearsal version   # 1.0.0
```

### Offline iron path

```bash
./bin/rehearsal snapshot k8s --dir examples/e2e-pipeline/cluster-dump --out baseline.json
./bin/rehearsal change manifests --baseline baseline.json \
  --dir examples/e2e-pipeline/rendered-chart --namespace payments --out change.json
./bin/rehearsal analyze --baseline baseline.json --change change.json --out out --quiet
# exit 3 = block

./bin/rehearsal snapshot k8s --dir examples/e2e-pipeline/observed-dump \
  --phase observed --meta examples/e2e-pipeline/observed-meta.json --out observed.json
./bin/rehearsal verify --report out/latest-report.json --observed observed.json \
  --baseline baseline.json --change change.json

# evidence chain
./bin/rehearsal evidence chain --baseline baseline.json --change change.json \
  --report out/latest-report.json --observed observed.json --out out/chain.json
```

### Full run lifecycle

```bash
./bin/rehearsal run execute \
  --baseline baseline.json --change change.json --observed observed.json \
  --out out/run.json
```

### Control plane API

```bash
./bin/rehearsal serve --addr :8080
curl -H "Authorization: Bearer local-dev" http://127.0.0.1:8080/v1/version
```

### Helm

```bash
helm upgrade --install rehearsal deploy/helm/architecture-rehearsal
```

---

## Exit codes

| Code | Meaning |
| ---: | ------- |
| 0 | approve / verified |
| 1 | warn / diverged |
| 2 | usage / authz |
| 3 | block |
| 4 | unknown / inconclusive |
| 5 | internal error |

---

## Docs

- [API](docs/api.md)
- [SLO](docs/slo.md)
- [Threat model](docs/threat-model.md)
- [Operations](docs/runbooks/operations.md)
- [CHANGELOG](CHANGELOG.md)

---

## License

Apache-2.0
