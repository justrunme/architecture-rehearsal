package scenario

import (
	"fmt"

	"github.com/justrunme/architecture-rehearsal/internal/graph"
)

// PDBDisruption detects drain/node loss that violates PodDisruptionBudget.
type PDBDisruption struct{}

func (PDBDisruption) Name() string { return "pdb-disruption" }

func (PDBDisruption) Run(ctx Context) []Finding {
	ch := ctx.Change
	if ch == nil || ctx.BaseIdx == nil {
		return nil
	}
	kind := ch.EffectiveKind()
	if kind != "node-failure" && kind != "node-drain" && factString(ch.Facts, "scenario", "") != "pdb-disruption" {
		return nil
	}
	// For each PDB, check if minAvailable cannot be satisfied after removing pods on lost node.
	var findings []Finding
	lost := map[string]bool{}
	for _, id := range ch.RemovedNodes {
		lost[id] = true
	}
	if ln := factString(ch.Facts, "lost_node", ""); ln != "" {
		lost[ln] = true
	}
	if len(lost) == 0 {
		return nil
	}
	for _, pdb := range ctx.BaseIdx.ByKind[graph.KindPDB] {
		minA := pdb.AttrInt("minAvailable")
		if minA <= 0 {
			continue
		}
		// Workloads protected by this PDB
		var workloads []string
		for _, e := range ctx.BaseIdx.In[pdb.ID] {
			if e.Rel == graph.RelProtectedBy {
				workloads = append(workloads, e.From)
			}
		}
		for _, e := range ctx.BaseIdx.Out {
			_ = e
		}
		// also PROTECTED_BY from workload -> pdb
		for wid, edges := range ctx.BaseIdx.Out {
			for _, e := range edges {
				if e.To == pdb.ID && e.Rel == graph.RelProtectedBy {
					workloads = append(workloads, wid)
				}
			}
		}
		workloads = unique(workloads)
		for _, wid := range workloads {
			w := ctx.BaseIdx.ByID[wid]
			if w == nil {
				continue
			}
			rep := w.WorkloadReplicas()
			// count pods on lost nodes (simplified: if workload RUNS_ON lost node)
			onLost := false
			for _, e := range ctx.BaseIdx.Out[wid] {
				if e.Rel == graph.RelRunsOn && lost[e.To] {
					onLost = true
				}
			}
			if !onLost {
				// Stateful single-replica often implied on bound node via PVC path
				if rep == 1 && (kind == "node-failure" || kind == "node-drain") {
					onLost = factString(ch.Facts, "scenario", "") != "" || len(lost) > 0
				}
			}
			if !onLost {
				continue
			}
			remaining := rep - 1
			if remaining < minA {
				findings = append(findings, Finding{
					ID:       "pdb-disruption:" + pdb.ID + ":" + wid,
					Scenario: "pdb-disruption",
					Risk:     "high",
					Title:    "PodDisruptionBudget would block or violate disruption",
					Summary: fmt.Sprintf(
						"PDB %s requires minAvailable=%d but workload %s would have %d pods after node loss (replicas=%d).",
						pdb.Name, minA, display(ctx.BaseIdx, wid), remaining, rep,
					),
					Components: []string{pdb.ID, wid},
					Cascade: []string{
						"node drain / loss",
						fmt.Sprintf("workload %s loses a pod", display(ctx.BaseIdx, wid)),
						fmt.Sprintf("remaining %d < minAvailable %d", remaining, minA),
						"eviction denied or availability SLO breach",
					},
					Controls: []string{
						"increase replicas before drain",
						"adjust PDB minAvailable for maintenance windows",
						"use voluntary disruption budget carefully with stateful sets",
					},
					SLOImpact:  "availability during maintenance",
					Evidence:   []string{fmt.Sprintf("minAvailable=%d", minA), fmt.Sprintf("replicas=%d", rep)},
					Rollback:   RollbackUnknown,
					Confidence: "medium",
				})
			}
		}
	}
	return findings
}
