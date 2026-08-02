# Architecture Rehearsal

**Know what breaks before you deploy.**

Operational architecture twin that predicts the **blast radius** of infrastructure changes before deployment and verifies the result afterward.

> Status: **v0.1.0** — snapshot-first golden scenarios (RWO node loss, CNI/IP capacity, Prometheus zero-match). Not a multi-cloud EA platform. Not an AI that manages clusters.

```text
Proposed change → live dependency graph → deterministic simulation
→ predicted blast radius → decision → (deploy) → verification evidence
```

**Graph and rules decide. AI (optional, later) only explains.**

---

## Why

CI can lint YAML and run unit tests. It rarely answers:

> What fails if I merge this MR, drain this node, scale this Helm chart, or ship this Prometheus rule?

Architecture Rehearsal turns **IaC + Kubernetes topology + observability schema** into a temporal graph and runs **deterministic failure patterns** against a proposed change.

---

## Quick start

```bash
git clone https://github.com/justrunme/architecture-rehearsal.git
cd architecture-rehearsal
make demo
```

Or one scenario:

```bash
make build
./bin/rehearsal analyze \
  --baseline examples/golden/rwo-node-loss/baseline.json \
  --change examples/golden/rwo-node-loss/change.json \
  --html out/rwo-report.html
```

Exit codes: `0` approve · `1` warn · `3` block (high/critical).

---

## Golden scenarios (v0.1)

| Scenario | What it models |
| -------- | -------------- |
| **RWO + node loss** | Stateful workload, RWO PVC bound to lost node, dependents go dark |
| **CNI / IP capacity** | Pod IP exhaustion → FailedCreatePodSandbox → Helm timeout cascade |
| **Prom zero-match** | Rule is valid PromQL but selectors match **zero** observed series |

These are sanitized forms of real operational failure classes (stateful reattach, AWS VPC CNI capacity, observability drift).

---

## Architecture (v0.1)

```text
examples/golden/*/baseline.json   # Deployed + observed snapshot
examples/golden/*/change.json     # Proposed change envelope
        │
        ▼
  graph load + apply change
        │
        ▼
  scenario engine (rules)
        │
        ▼
  report.json + report.html + evidence bundle
```

### Four conceptual states

| State | In v0.1 |
| ----- | ------- |
| Intended | (next) ADR / SLO as first-class nodes |
| Desired | change envelope (plan/diff facts) |
| Deployed | baseline snapshot nodes/edges |
| Observed | meta: metrics, label schema, capacity |

---

## CLI

```bash
rehearsal analyze --baseline FILE --change FILE [--out DIR] [--html PATH] [--quiet]
rehearsal version
```

Example JSON (shape):

```json
{
  "risk": "critical",
  "decision": "block",
  "affected_components": 5,
  "predicted_failures": ["rwo-node-loss"],
  "findings": [ ... ],
  "coverage_gaps": [ "partial graph: IAM ..." ]
}
```

---

## What this is not (v0.1)

- Not multi-cloud inventory
- Not a Kubernetes operator
- Not a GitHub App / auto-merge bot
- Not a full Enterprise Architecture catalog
- Not “AI decides risk”

Honest coverage gaps are always printed. **Incomplete graphs must not claim certainty.**

---

## Roadmap (thin)

1. `rehearsal verify` — post-deploy snapshot vs prediction score  
2. Real importers: `kubectl` snapshot, Terraform plan JSON, Prometheus labels API  
3. More scenarios: PDB + disruption, HPA thrash, IRSA/IAM blast radius  
4. Optional LLM layer for change summary / ADR draft only  

---

## License

Apache-2.0
