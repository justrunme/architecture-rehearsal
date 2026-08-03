# Changelog

## 1.5.0 — 2026-08-03

**Kubernetes Operator** — controller-runtime RehearsalRun CR → control plane.

### Operator
- CRD `rehearsalruns.rehearsal.io/v1beta1` (`config/crd/`)
- Binary `cmd/rehearsal-operator` (controller-runtime manager)
- Reconciles CR → HTTP control plane create + advance + status sync
- Env: `REHEARSAL_API_URL`, `REHEARSAL_API_TOKEN`

```bash
kubectl apply -f config/crd/rehearsal.io_rehearsalruns.yaml
go run ./cmd/rehearsal-operator
```

## 1.4.1 — 2026-08-03

**Hardening** — evidence recompute, cancel safety, strict OIDC, PG legacy, release workflow.

### Evidence (P0)
- `ComputeSemanticDigest` exported; VerifyChain **always recomputes** report body
- Tamper tests: decision/risk/findings/rollback/predictedFailures with stale semanticDigest fail

### Durability
- PostgreSQL legacy migration: global `id` PK → `(org,id)` + org-scoped idempotency
- CI: `pg_legacy_smoke` after legacy seed
- Schema v4: optimistic `runs.version`
- Atomic enqueue: INSERT then SELECT on unique conflict (no TOCTOU)

### Cancellation
- Engine `ExecuteContext` cancels between steps
- Worker watches job status; stops on cancel; **fence check before UpdateRun**

### OIDC
- `golang-jwt/jwt/v5` RS256 + JWKS
- Requires issuer, audience, exp, sub, org claim
- Unit tests for missing claims

### S3 / docs
- `REHEARSAL_S3_*` wires MinIO path-style blob (reference; not full AWS SigV4)
- Capability matrix honest; Helm image.tag drift fixed

### Release
- `.github/workflows/release.yml` publishes binaries + SHA256SUMS + SBOM on tag

## 1.4.0 — 2026-08-03

**Platform hooks** — operator ↔ control plane, GitHub/GitLab status adapters.

### Operator
- `rehearsal operator --api URL --token TOK` syncs RehearsalRun JSON CR → HTTP control plane
- Local engine path retained when `--api` omitted

### CI status
- `rehearsal status github|gitlab --sha --state|--decision|--exit-code`
- Library: `internal/integrations/status` (Checks/Commit Status)

### Gate references
- GitHub Actions / GitLab CI examples point at v1.4.0

## 1.3.0 — 2026-08-03

**Operational control plane** — jobs, audit, OIDC, metrics, S3 blobs.

### Job API
- `GET /v1/jobs`, `GET /v1/jobs/{id}`, `POST .../cancel`, `POST .../retry`
- Exactly-once enqueue via operation ID (unchanged) + cancel/retry

### Audit API
- `GET /v1/audit?limit=` tenant-filtered

### Auth
- Optional **real OIDC/JWKS** (`REHEARSAL_OIDC_ISSUER`, `REHEARSAL_OIDC_JWKS_URL`, `REHEARSAL_OIDC_AUDIENCE`)
- Static tokens remain for self-hosted; unsigned JWT never accepted

### Ops
- Pagination `limit` on list runs
- Metrics: `rehearsal_queue_depth`, `rehearsal_schema_version`
- Readyz checks DB + blob
- S3-compatible blob interface (`persist.S3Blob`) for multi-replica

## 1.2.2 — 2026-08-03

**Durable means durable** — migrations, Postgres CI, backup, concurrent workers.

### Storage
- Versioned migrations (schema v3 indexes)
- `rehearsal backup --db --out` SQLite backup smoke
- Concurrent worker claim test (exactly one lease)

### CI
- Postgres service job: Open / CreateRun / ClaimJob / Complete with fence

## 1.2.1 — 2026-08-03

**Trust Boundaries + Durability** — production isolation and evidence integrity.

### Multi-tenant write isolation (P0)
- Run primary key is **`(org, id)`** — same run ID allowed across tenants, never overwrites another org
- Create returns **409 Conflict** on duplicate `(org, id)` or idempotency key in the same org
- Idempotency unique on **`(org, idempotency_key)`** — one tenant cannot block another’s key
- All run/job access is org-scoped (`GetRun(org,id)`, enqueue, cancel)

### Evidence chain (P0)
- `VerifyChain` **always recomputes** live baseline/change/report/observed digests
- Embedded report digests alone are **not trusted**; live mismatch always fails
- Mutation tests: baseline, change, report digest, observed, lying bindings

### Workspace sandbox (P1)
- `serve` **requires `--workdir`** (refuses to start without it)
- Blocks absolute escape, `..`, and **symlink escape** outside root
- Ready checks backend (DB) + workdir

### Calibration (P1)
- Calibration rows are **org-scoped**; `/v1/calibration` returns only caller org

### Policy snapshots (P1)
- Per-run immutable policy file under `policies/<org>/snapshots/<digest>-<runId>.yaml`
- Run labels carry `policyDigest`

### Jobs / leases (P1)
- Lease TTL default **15m** (covers 10m run timeout) + **heartbeat renew**
- **Fencing token** on claim; stale complete/fail rejected
- Cancel advances cancel pending/leased jobs for the run
- Operation ID for exactly-once logical enqueue

### Helm durability (P1)
- **PVC** by default (`persistence.enabled=true`) instead of emptyDir
- Postgres via `REHEARSAL_DATABASE_URL` **wins** over `--db` (no SQLite override when PG set)

## 1.2.0 — 2026-08-03

**Real Control Plane** — durable storage, async jobs, metrics.

### Storage
- SQLite default (`--db path` or `data/rehearsal.db`); optional Postgres (`postgres://…` / `REHEARSAL_DATABASE_URL`)
- Content-addressed blob store (`--blob DIR` / `REHEARSAL_BLOB_ROOT`) for evidence artifacts
- Backend interface: memory (dev/tests) or SQL

### Async jobs
- Durable job queue with lease claim / complete / fail + retry budget
- Background workers (`--async --workers N`) execute `run.advance`
- `POST /v1/runs/{id}/advance` returns **202** with `jobId` when async

### API
- `GET /v1/metrics` Prometheus text (requests, jobs, uptime)
- Org policy binding: active policy written to workdir for engine `PolicyPath`
- Calibration + audit persisted in SQL

### CLI
```
rehearsal serve --addr :8080 --workdir . --db data/rehearsal.db --blob data/blobs --async --workers 2
rehearsal serve --memory --insecure-dev   # non-durable local only
```

### Security note
- Authn/authz from v1.0.1 still required. Networked API without token is refused.

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
