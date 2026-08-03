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

**Status: v1.2.0** — durable control plane (SQLite/Postgres + blobs + async jobs).  
Evidence integrity (v1.1) + secure API (v1.0.1+) remain required for production gates.  
**v1.0.0 network API was unsafe** — use ≥1.0.1 for any networked `serve`.

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
| Control plane HTTP API | **Supported** (v1.0.1+ secure token required) |
| Durable SQL store | **Supported** (SQLite default; Postgres URL) |
| Content-addressed blobs | **Supported** (local filesystem; S3-shaped API later) |
| Async job workers + leases | **Supported** (`--async`) |
| Hierarchical RBAC + bearer auth | **Supported** (org-scoped; no OIDC stub) |
| GitOps GH/GL full gate | **Reference** skeleton only |
| Calibration engine | **Supported** (SQL-backed in control plane) |
| Scenario package registry / SDK surface | **Supported** |
| Helm chart | **Supported** (`deploy/helm`) |
| Live kubectl collect | **Supported** |
| Live controller-runtime operator | **Reference** (JSON reconciler) |
| Multi-tenant SaaS | **Not supported** |
| LLM in risk path | **Not supported** |

---

## Quick start

```bash
git clone https://github.com/justrunme/architecture-rehearsal.git
cd architecture-rehearsal
make demo && make e2e
make build && ./bin/rehearsal version   # 1.2.0
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

# evidence chain (also written automatically by analyze as out/*/evidence-chain.json)
./bin/rehearsal evidence chain --baseline baseline.json --change change.json \
  --report out/latest-report.json --observed observed.json --out out/chain.json

# sign & verify DSSE (requires REHEARSAL_HMAC_SECRET)
export REHEARSAL_HMAC_SECRET="$(openssl rand -hex 32)"
./bin/rehearsal evidence sign-dsse --chain out/latest-chain.json --out out/evidence-dsse.json
./bin/rehearsal evidence verify-dsse --envelope out/evidence-dsse.json
```

### Full run lifecycle

```bash
./bin/rehearsal run execute \
  --baseline baseline.json --change change.json --observed observed.json \
  --out out/run.json
```

### Control plane API (v1.2)

```bash
# REQUIRED: strong token (serve refuses to start without it)
export REHEARSAL_API_TOKEN="$(openssl rand -hex 32)"
export REHEARSAL_API_ORG=my-org

# Durable default: SQLite + local blobs under workdir
./bin/rehearsal serve --addr :8080 --workdir "$PWD" \
  --db "$PWD/data/rehearsal.db" --blob "$PWD/data/blobs" --async --workers 2

# Postgres (optional):
# export REHEARSAL_DATABASE_URL='postgres://user:pass@localhost/rehearsal'
# ./bin/rehearsal serve --workdir "$PWD" --async

curl -H "Authorization: Bearer $REHEARSAL_API_TOKEN" http://127.0.0.1:8080/v1/version
curl -H "Authorization: Bearer $REHEARSAL_API_TOKEN" http://127.0.0.1:8080/v1/metrics

# non-durable local only:
# ./bin/rehearsal serve --memory --insecure-dev --addr :8080
```

**GitOps status:** `integrations/github-actions/gate.yml` is a **Reference** skeleton (not a full PR gate yet).

### Helm

```bash
helm upgrade --install rehearsal deploy/helm/architecture-rehearsal \
  --set api.token="$(openssl rand -hex 32)" \
  --set image.tag=1.2.0
# chart fails closed if api.token is empty
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
