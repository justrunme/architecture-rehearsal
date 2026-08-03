# Changelog

## 0.7.1 — 2026-08-03

**CI hotfix** — golden multi-scenario verify.

- Component presence only for primary survivors (`workload/*`, `pvc/*`) — not lost nodes / cascade svc/pdb/slo
- Independent predicates for `volume-az` and `pdb-disruption` (workload Pending markers, PVC zone/boundNode)
- Golden `rwo-node-loss/observed.json` includes Pending Pod + full `observed_failures`
- Regression test for CI verify loop

## 0.7.0 — 2026-08-03

**Platform layer** (local control-plane primitives — not a hosted SaaS).

### Run store + audit
- `internal/store` filesystem run records (`out/runs/runs/*.json`)
- Append-only `audit.jsonl`
- CLI: `rehearsal store list|save`, `rehearsal audit`
- `analyze --store DIR` persists each gate run

### Signed evidence
- HMAC-SHA256 envelope (`REHEARSAL_HMAC_SECRET`)
- CLI: `rehearsal sign`, `analyze --sign-out FILE`
- Verify helper for envelopes (not Sigstore/cosign yet)

### Multi-cluster CLI
- `rehearsal merge --name fleet snap1.json snap2.json …` (uses `graph.MergeSnapshots`)

### RBAC config model
- Role/action policy (`internal/rbac`) for local actor gating
- Env `REHEARSAL_ACTOR` (default `local`)

### Honesty
- Still not: network authn service, multi-tenant SaaS, published GHCR multi-arch product

## 0.6.0 — 2026-08-03

**Live rehearsal + graph fidelity**

### Live collect
- `snapshot k8s --live [--kubeconfig] [--context]` via read-only `kubectl get -A -o yaml`
- Never writes to the cluster; reuses offline parser

### Graph fidelity
- Pod ownership prefers `ownerReferences` (Pod→ReplicaSet→Deployment)
- ReplicaSet owner map recorded for chain resolution
- PV zone from CSI labels **and** `nodeAffinity` matchExpressions
- EndpointSlice ready counts annotated onto Service (`readyEndpoints`)
- PDB `minAvailable: 50%` / `maxUnavailable` percentage support in collector + scenario

### Capacity model
- `internal/capacity`: `SchedulingEstimate` vs explicit `cni_ip_available`
- Meta: `pod_scheduling_capacity_estimate` + `capacity_model`

## 0.5.0 — 2026-08-03

**Independent observation** (verify no longer trusts operator annotations as proof).

- Scenario-specific observed predicates (CNI pending pods, RWO Pending Pods scoped to components/PVCs)
- `meta.observed_failures` is **soft** annotation only
- Deployed change identity + digest from patch nodes
- Baseline→observed delta checks
- CLI: `verify --baseline --change`
- All predicted scenarios need their own evidence (no single-match short-circuit)

## 0.4.1 — 2026-08-03

**Trust patch** — fail-closed ingestion.

- Collector + manifest compiler **strict by default**
- `--allow-partial` opt-in only
- `coverage_gap` / `yaml_parse_errors` → `RequiredMissing` → never APPROVE
- Negative tests: malformed YAML, partial dir, unsupported Job
- RWO verify looks at **KindPod** (not Workload)
- Component presence no longer free-passes score

## 0.4.0 — 2026-08-03

**End-to-end iron path** (one real pipeline, no new scenarios).

### E2E pipeline
- Fixture tree `examples/e2e-pipeline/`: kubectl List dump → rendered helm chart → observed dump + meta
- `scripts/e2e_pipeline.sh` + `make e2e`: dump → `snapshot k8s` → scoped `change manifests` → analyze (**block**) → observed snapshot → verify (**verified**)
- Go integration test `internal/e2e` covering the same path without shell

### Collectors / CLI
- `snapshot k8s --phase baseline|observed|deployed`
- `snapshot k8s --meta FILE` merges operator/CI annotations after capacity derivation
- Observed phase marks Pending pods `unschedulable` for verify helpers

### Change compiler
- Manifest diffs set `facts.scenario=cni-ip-capacity` so capacity rules always evaluate on helm/manifest scale paths

## 0.3.0 — 2026-08-03

**Real graph pipeline** (honest milestone after prototype inflation).

### Collectors
- Recursive YAML directories
- Kubernetes `kind: List` / `items[]`
- Default namespace normalization
- Nodes: Pod, PV, Ingress, ServiceAccount, HPA, PDB, …
- Edges: Workload→PVC, Service→Workload (selector), PDB→Workload, HPA→Workload, Pod→Node, PVC→PV+zone
- Capacity meta from node allocatablePods
- Fail-closed `validate.Snapshot` on collect

### Change compilers
- Manifest diff **scoped** by `--namespace` / prefix
- Removals **disabled by default** (`--allow-remove` opt-in within scope)
- Recursive rendered dirs + List support
- Terraform seeds use baseline node/cluster IDs (no invalid `tf:` seeds)

### Scenarios
- Interface: Applicable / MissingRequirements / Evaluate (`matched|not_matched|unknown`)
- Confident findings still **block**; missing prereqs → **unknown** (never false approve for that path)
- CNI maxSurge uses Kubernetes-style **ceil** for percentages
- PDB only when RUNS_ON lost node (fewer false positives)
- Positive/negative/unknown tests for core scenarios + collector/compiler tests

### Versioning
- Retract premature `v1.0.0` / `v2.0.0` tags
- Binary version `0.3.0`

## 0.2.0 — 2026-08-03

Correctness foundation (formerly mis-tagged as v1.0 in a single commit):

- Deep-copy ApplyChange, rollback ternary, decision unknown, evidence SHA-256
- Validation, six scenarios (fixture-driven), verify prototype, distroless Dockerfile reference
- Public fixtures: `acme-prod` only

## 0.1.0 — 2026-08-02

Initial golden prototype (RWO, CNI, Prom).
