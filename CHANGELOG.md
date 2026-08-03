# Changelog

## 1.1.0 — 2026-08-03

**Evidence Integrity** — product-grade binding of predictions to artifacts.

### Report / verify binding
- `ImpactReport` embeds `baselineDigest`, `changeDigest`, `proposedDigest`
- Semantic digest includes those bindings
- `verify` **refuses** when report digests ≠ live baseline/change (`report_binding`)
- Verification result records artifact digests

### DSSE / chain
- `EvidenceStatement` payload includes chain digests + keyId + signedAt (inside signature)
- `analyze` writes `evidence-chain.json` (+ `evidence-dsse.json` when `REHEARSAL_HMAC_SECRET` set)
- `rehearsal evidence sign-dsse --chain` / `verify-dsse --envelope`
- Run engine **persists** chain + verification JSON under `out/<runId>/`

### Live causal data
- Collector ingests Kubernetes **Events** and promotes reasons onto Pods
- Live dump includes `events`
- Workload status: `readyReplicas` / `availableReplicas` / `unavailableReplicas`
- Incomplete rollout under CNI prediction is soft (consistent with failure prediction)

### Gate / calibration
- Run gate evaluates organization **policy** (`Spec.PolicyPath`)
- Per-scenario calibration outcomes (not fake `gate` aggregate)

### API
- `/v1/runs/{id}/evidence` returns digests + inline chain/DSSE when present

## 1.0.1 — 2026-08-03

**Security correction** — networked API tenant/auth hard-fix.

> **v1.0.0 is not safe for network deployment.** Use **v1.0.1+** for any exposed API.

### Authn
- Removed hardcoded `ci`, `viewer-token`, default `local-dev` tokens
- `serve` **refuses to start** without `REHEARSAL_API_TOKEN` / `REHEARSAL_API_TOKENS` (or explicit `--insecure-dev`)
- Removed OIDC stub that accepted any `a.b.c` JWT when issuer env set
- Client `X-Org` **cannot** override principal organization

### Authz / tenancy
- Object-level authorization after load (`get/advance/evidence`)
- Cross-tenant reads return **404** (no existence leak)
- Clusters/policies filtered by org labels
- Run create binds `labels.org` from principal only

### API hardening
- WorkDir / path refs sandboxed under `--workdir` root
- Request body size limit; HTTP server timeouts
- Cross-tenant + hardcoded-token negative tests

### Helm
- Secret template **required**; chart fails without `api.token`
- `optional: false` on token secretKeyRef
- Image tag `1.0.1`

## 1.0.0 — 2026-08-03

**YANKED for network use** — offline CLI is fine; API had tenant/auth bypass (hardcoded tokens + `X-Org` principal rewrite + missing object authz). Fixed in 1.0.1.

Original notes:

**Stable production contract** for the Architecture Rehearsal control plane.

**Stable production contract** for the Architecture Rehearsal control plane.

> Deterministic pre-deployment failure simulation and post-deployment verification control plane.
> Builds a causal architecture graph, predicts change impact, blocks unsafe deployments,
> verifies observed outcomes, and measures scenario accuracy.

### Pipeline

```text
Collect → Model → Propose → Rehearse → Gate → Observe → Verify → Calibrate
```

### Included milestones (0.8 → 1.0)

#### v0.8 Evidence contracts
- JSON Schema catalog (`schemas/v1beta1/`)
- API versions: `v1alpha1` / `v1beta1` / `v1` + migrations
- Content digests: baseline/change/proposed/report/observed/verification
- Evidence chain; verify refuses mismatched digests
- DSSE-style envelopes (HMAC + Ed25519); `rehearsal evidence sign-dsse`

#### v0.9 Live rehearsal runner
- `RehearsalRun` state machine (Pending…Completed/Failed/Inconclusive)
- Idempotency keys, leases, timeouts
- Offline engine: `rehearsal run execute`
- CRDs under `deploy/crds/`
- Operator reconcile loop: `rehearsal operator --watch`

#### v0.10 GitOps change gate
- Policy engine (CEL-like block/warn rules): `rehearsal policy`
- GitHub Actions gate reference: `integrations/github-actions/gate.yml`

#### v0.11 Control plane API
- HTTP API: `rehearsal serve` (`POST /v1/runs`, clusters, policies, calibration)
- In-memory store with audit (PostgreSQL/S3 interfaces documented)
- Idempotent run creation

#### v0.12 Security
- Bearer auth + optional OIDC stub mode
- Hierarchical RBAC (org → project → environment)
- Threat model: `docs/threat-model.md`
- No inline kubeconfig secrets

#### v0.13 Calibration + Scenario SDK
- True/false positive tracking, precision/recall
- Deterministic confidence factors
- `rehearsal calibrate --demo`
- Scenario package metadata registry

#### v1.0 Production contract
- Helm chart `deploy/helm/architecture-rehearsal`
- SLO + runbooks + API docs
- Multi-arch release asset scripts + SBOM helper
- CI: API smoke, policy, docker build

### Honesty boundaries
- Not multi-tenant SaaS with per-tenant KMS
- Not full Sigstore keyless (DSSE + HMAC/Ed25519 path provided)
- Not a full CRD operator in-cluster controller-runtime binary (reconciler JSON loop shipped)
- Memory store default; PostgreSQL/S3 optional production swap

## 0.7.2 — 2026-08-03

**Verification Integrity** — causal predicates, honest identity, production mode.

## 0.7.1 — 2026-08-03

Golden multi-scenario verify CI hotfix.

## 0.7.0 — 2026-08-03

Platform primitives (store, HMAC, merge, live kubectl, ownerRefs).

## 0.4.0 — 2026-08-03

Iron E2E path dump → graph → scoped change → verify.

## 0.3.0 — 2026-08-03

Real offline graph pipeline.

## 0.2.0 — 2026-08-03

Correctness foundation.

## 0.1.0 — 2026-08-02

Initial golden prototype.
