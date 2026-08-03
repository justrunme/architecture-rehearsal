package scenario

import (
	"fmt"
	"math"

	"github.com/justrunme/architecture-rehearsal/internal/graph"
)

// effectiveMinAvailable supports integer and percentage minAvailable (e.g. "50%").
func effectiveMinAvailable(pdb *graph.Node, replicas int) int {
	if pdb == nil {
		return 0
	}
	if pct := pdb.AttrInt("minAvailablePercent"); pct > 0 && replicas > 0 {
		// Kubernetes rounds up for minAvailable percentages.
		return int(math.Ceil(float64(replicas) * float64(pct) / 100.0))
	}
	if n := pdb.AttrInt("minAvailable"); n > 0 {
		return n
	}
	return 0
}

type PDBDisruption struct{}

func (PDBDisruption) Name() string { return "pdb-disruption" }

func (PDBDisruption) Applicable(ctx Context) bool {
	ch := ctx.Change
	if ch == nil {
		return false
	}
	k := changeKind(ch)
	return k == "node-failure" || k == "node-drain" || factString(ch.Facts, "scenario", "") == "pdb-disruption"
}

func (PDBDisruption) MissingRequirements(ctx Context) []Requirement {
	var miss []Requirement
	if !hasKind(ctx.BaseIdx, graph.KindPDB) {
		// Not applicable as unknown — if no PDBs, scenario simply not matched
		return nil
	}
	// need PROTECTED_BY edges
	has := false
	if ctx.Baseline != nil {
		for _, e := range ctx.Baseline.Edges {
			if e.Rel == graph.RelProtectedBy {
				has = true
				break
			}
		}
	}
	if !has {
		miss = append(miss, Requirement{ID: "pdb-edges", Message: "PDB nodes exist but no PROTECTED_BY edges to workloads"})
	}
	return miss
}

func (r PDBDisruption) Evaluate(ctx Context) Result {
	if !hasKind(ctx.BaseIdx, graph.KindPDB) {
		return Result{Outcome: OutcomeNotMatched}
	}
	ch := ctx.Change
	lost := map[string]bool{}
	for _, id := range ch.RemovedNodes {
		lost[id] = true
	}
	if ln := factString(ch.Facts, "lost_node", ""); ln != "" {
		lost[ln] = true
	}
	if len(lost) == 0 {
		return Result{Outcome: OutcomeNotMatched}
	}
	var findings []Finding
	for _, pdb := range ctx.BaseIdx.ByKind[graph.KindPDB] {
		var workloads []string
		for wid, edges := range ctx.BaseIdx.Out {
			for _, e := range edges {
				if e.To == pdb.ID && e.Rel == graph.RelProtectedBy {
					workloads = append(workloads, wid)
				}
			}
		}
		for _, wid := range unique(workloads) {
			w := ctx.BaseIdx.ByID[wid]
			if w == nil {
				continue
			}
			rep := w.WorkloadReplicas()
			minA := effectiveMinAvailable(pdb, rep)
			if minA <= 0 {
				continue
			}
			onLost := false
			for _, e := range ctx.BaseIdx.Out[wid] {
				if e.Rel == graph.RelRunsOn && lost[e.To] {
					onLost = true
				}
			}
			// only if explicitly scheduled on lost node
			if !onLost {
				continue
			}
			remaining := rep - 1
			if remaining < minA {
				findings = append(findings, Finding{
					ID: "pdb-disruption:" + pdb.ID + ":" + wid, Scenario: r.Name(), Risk: "high",
					Title: "PodDisruptionBudget would block or violate disruption",
					Summary: fmt.Sprintf("PDB %s minAvailable=%d; workload %s would have %d after node loss", pdb.Name, minA, display(ctx.BaseIdx, wid), remaining),
					Components: []string{pdb.ID, wid},
					Cascade: []string{"node loss", fmt.Sprintf("remaining %d < minAvailable %d", remaining, minA), "eviction denied or SLO breach"},
					Controls: []string{"increase replicas before drain", "adjust PDB for maintenance"},
					SLOImpact: "availability", Evidence: []string{fmt.Sprintf("minAvailable=%d raw=%s", minA, pdb.AttrString("minAvailableRaw"))},
					Rollback: RollbackUnknown, Confidence: "medium",
				})
			}
		}
	}
	if len(findings) == 0 {
		return Result{Outcome: OutcomeNotMatched}
	}
	return Result{Outcome: OutcomeMatched, Findings: findings}
}
