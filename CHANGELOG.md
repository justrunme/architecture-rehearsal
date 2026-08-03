# Changelog

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
