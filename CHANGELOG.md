# Changelog

## 2.0.0 — 2026-08-03

- Multi-cluster snapshot merge (`graph.MergeSnapshots`) for multi-repo / multi-cluster baselines
- Cluster-prefixed node IDs and capacity facts namespacing

## 1.0.0 — 2026-08-03

Production self-hosted change gate:

- Fail-closed validation (duplicate nodes, dangling edges, bad seeds/patches)
- Decision model: `approve | warn | block | unknown`
- Rollback: `available | unavailable | unknown`
- Evidence bundles with SHA-256 (fail-closed writes)
- Deterministic semantic digest
- CNI capacity derived from baseline→proposed replicas + maxSurge
- Metric-specific Prometheus label schema
- Scenarios: RWO, CNI, Prom zero-match, PDB, service routing, volume AZ
- CLI: `analyze`, `verify`, `snapshot k8s`, `change manifests|terraform`
- Distroless non-root container
- GitHub Actions / GitLab CI examples
- Deep-copy ApplyChange (baseline immutability)
- Public fixtures use `acme-prod` only

## 0.1.0 — 2026-08-02

Initial prototype: three golden scenarios, CLI analyze, HTML report.
