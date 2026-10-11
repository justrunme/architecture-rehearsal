# Argo CD change-impact gate

Architecture Rehearsal explains **consequences** of an Argo CD sync. It does **not** re-implement Argo’s diff engine.

```text
Argo Application
  desired  ← argocd app manifests --revision | rendered dir | repo-server/helm template
  live     ← cluster snapshot (baseline)
       ↓
ChangeEnvelope (kind: argo-sync)
       ↓
Causal graph + impact detectors
       ↓
Markdown PR report + JSON + signed evidence + GitHub/GitLab commit status
```

## Detectors

Each detector returns **PASS / WARN / BLOCK / UNKNOWN** with explanation and confidence.

| Detector | Concern |
| -------- | ------- |
| `scheduling-capacity` | New replicas vs node allocatable; topologySpreadConstraints; affinity/anti-affinity; taints |
| `pvc-zone-conflict` | PVC vs node affinity / storage class zone conflicts |
| `networkpolicy-cut` | NetworkPolicy change cutting a known `DEPENDS_ON` edge (e.g. app → postgres) |
| `pdb-rollout` | PDB violation by the rollout (`maxUnavailable` vs `minAvailable`) |
| `blast-radius` | Other workloads affected via graph edges |

**Rule:** missing data → **UNKNOWN**, never silent PASS (ADR-0001).

## CLI

```bash
# Live Argo CD Application
rehearsal argo-impact --app my-app --revision $SHA --live --out out/argo-impact

# Offline / CI fixtures (same compile path)
rehearsal argo-impact --app demo --desired-dir ./desired --baseline baseline.json --out out/argo-impact

# Post commit status / check summary when GITHUB_TOKEN + GITHUB_REPOSITORY are set
rehearsal argo-impact --app my-app --desired-dir ./desired --baseline baseline.json \
  --post-status --sha "$GITHUB_SHA" --out out/argo-impact
```

Exit codes match the decision model: `0` PASS · `1` WARN · `3` BLOCK · `4` UNKNOWN.

## GitHub Action

See [`integrations/github-actions/argo-impact.yml`](../integrations/github-actions/argo-impact.yml).

## Real gate output (fixture scenarios)

Captured from `rehearsal argo-impact` against `examples/e2e-argo/*` (same assertions as CI).

### Results

```
unschedulable: verdict=BLOCK … exit=3
netpol-cut:    verdict=BLOCK … exit=3
pdb-violation: verdict=BLOCK … exit=3
benign:        verdict=PASS  … exit=0
```

### Unschedulable replicas → BLOCK

```
# Argo CD change-impact report

- **Application:** `demo-unschedulable`
- **Gate verdict:** **BLOCK**
- **Analyze decision:** `block` (risk=high)

## Summary

gate=BLOCK detectors: PASS=3 WARN=1 BLOCK=1 UNKNOWN=0

## Detectors

| Detector | Status | Confidence | Explanation |
|----------|--------|------------|-------------|
| `blast-radius` | **WARN** | medium | blast radius includes 1 dependent workload |
| `networkpolicy-cut` | **PASS** | high | no NetworkPolicy changes in the proposed sync |
| `pdb-rollout` | **PASS** | high | no PodDisruptionBudgets in graph |
| `pvc-zone-conflict` | **PASS** | high | no PVCs in proposed graph |
| `scheduling-capacity` | **BLOCK** | high | unschedulable: need 8 pods (delta 6 + surge 2) but allocatable capacity is 2 (pod_scheduling_capacity_estimate) |
```

### NetworkPolicy cutting DB → BLOCK

```
verdict=BLOCK decision=block findings=1 confidence=high
```

Detector `networkpolicy-cut` returns **BLOCK** when an egress-deny (or equivalent) policy would cut a `DEPENDS_ON` edge such as `api → postgres`.

### PDB violation → WARN/BLOCK

```
verdict=BLOCK decision=block findings=2 confidence=medium
```

### Benign change → PASS

```
verdict=PASS decision=approve findings=0 confidence=high
```

## CI

- Offline iron path: `bash scripts/e2e_argo_impact.sh`
- Kind + Argo CD installed: `bash scripts/kind-argo-impact-e2e.sh` (job `argo-kind-e2e`)

Kind e2e installs Argo CD, creates a demo `Application`, then asserts the four scenarios above with **no failure masking**.

## Evidence & status

The command reuses:

- `analyze` report + `evidence.Bundle` (content-addressed)
- optional DSSE/HMAC when secrets are configured
- `rehearsal status github|gitlab` / `--post-status` for commit checks
