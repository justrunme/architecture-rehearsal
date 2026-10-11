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

## Real gate output (CI iron path)

Captured verbatim from `bash scripts/e2e_argo_impact.sh` (CI job step **Argo impact gate**). Same assertions as Kind + Argo CD e2e.

### Scenario exits

```
unschedulable exit=3 expect=3
netpol-cut exit=3 expect=3
pdb-violation exit=3 expect=1|3
benign exit=0 expect=0
```

### Unschedulable replicas → BLOCK

```markdown
# Argo CD change-impact report

- **Application:** `demo-unschedulable`
- **Change ID:** `argo-demo-unschedulable`
- **Gate verdict:** **BLOCK**
- **Confidence:** medium
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

## Affected workloads

- `workload/demo/api`
- `workload/demo/postgres`

## Predicted failures

- [WARN] blast radius includes 1 dependent workload
- [BLOCK] unschedulable: need 8 pods (delta 6 + surge 2) but allocatable capacity is 2 (pod_scheduling_capacity_estimate)

## Missing data

_Required coverage present for evaluated detectors._

---
_Architecture Rehearsal explains consequences of the desired sync; it does not re-implement Argo CD diff._
```

### NetworkPolicy cutting DB → BLOCK

```markdown
# Argo CD change-impact report

- **Application:** `demo-netpol-cut`
- **Change ID:** `argo-demo-netpol-cut`
- **Gate verdict:** **BLOCK**
- **Confidence:** high
- **Analyze decision:** `block` (risk=high)

## Summary

gate=BLOCK detectors: PASS=4 WARN=0 BLOCK=1 UNKNOWN=0

## Detectors

| Detector | Status | Confidence | Explanation |
|----------|--------|------------|-------------|
| `blast-radius` | **PASS** | high | no additional workloads in blast radius beyond change seeds |
| `networkpolicy-cut` | **BLOCK** | high | NetworkPolicy change cuts a known dependency edge (e.g. app → database) |
| `pdb-rollout` | **PASS** | high | no PodDisruptionBudgets in graph |
| `pvc-zone-conflict` | **PASS** | high | no PVCs in proposed graph |
| `scheduling-capacity` | **PASS** | high | no additional replica or surge demand vs baseline |

## Affected workloads

- `netpol/demo/deny-api-egress`
- `workload/demo/api`
- `workload/demo/postgres`

## Predicted failures

- [BLOCK] NetworkPolicy change cuts a known dependency edge (e.g. app → database)

## Missing data

_Required coverage present for evaluated detectors._

---
_Architecture Rehearsal explains consequences of the desired sync; it does not re-implement Argo CD diff._
```

### PDB violation → WARN/BLOCK

```markdown
# Argo CD change-impact report

- **Application:** `demo-pdb-violation`
- **Change ID:** `argo-demo-pdb-violation`
- **Gate verdict:** **BLOCK**
- **Confidence:** medium
- **Analyze decision:** `block` (risk=high)

## Summary

gate=BLOCK detectors: PASS=3 WARN=1 BLOCK=1 UNKNOWN=0

## Detectors

| Detector | Status | Confidence | Explanation |
|----------|--------|------------|-------------|
| `blast-radius` | **WARN** | medium | blast radius includes 1 dependent workload |
| `networkpolicy-cut` | **PASS** | high | no NetworkPolicy changes in the proposed sync |
| `pdb-rollout` | **BLOCK** | high | rollout would violate PodDisruptionBudget (maxUnavailable vs minAvailable) |
| `pvc-zone-conflict` | **PASS** | high | no PVCs in proposed graph |
| `scheduling-capacity` | **PASS** | high | no additional replica or surge demand vs baseline |

## Affected workloads

- `pdb/demo/api-pdb`
- `workload/demo/api`

## Predicted failures

- [WARN] blast radius includes 1 dependent workload
- [BLOCK] rollout would violate PodDisruptionBudget (maxUnavailable vs minAvailable)

## Missing data

_Required coverage present for evaluated detectors._

---
_Architecture Rehearsal explains consequences of the desired sync; it does not re-implement Argo CD diff._
```

### Benign change → PASS

```markdown
# Argo CD change-impact report

- **Application:** `demo-benign`
- **Change ID:** `argo-demo-benign`
- **Gate verdict:** **PASS**
- **Confidence:** high
- **Analyze decision:** `approve` (risk=none)

## Summary

gate=PASS detectors: PASS=5 WARN=0 BLOCK=0 UNKNOWN=0

## Detectors

| Detector | Status | Confidence | Explanation |
|----------|--------|------------|-------------|
| `blast-radius` | **PASS** | high | empty change; no blast radius |
| `networkpolicy-cut` | **PASS** | high | no NetworkPolicy changes in the proposed sync |
| `pdb-rollout` | **PASS** | high | no PodDisruptionBudgets in graph |
| `pvc-zone-conflict` | **PASS** | high | no PVCs in proposed graph |
| `scheduling-capacity` | **PASS** | high | no additional replica or surge demand vs baseline |

## Affected workloads

_None beyond change seeds (or not determined)._

## Predicted failures

_None predicted._

## Missing data

_Required coverage present for evaluated detectors._

---
_Architecture Rehearsal explains consequences of the desired sync; it does not re-implement Argo CD diff._
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
