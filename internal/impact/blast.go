package impact

import (
	"fmt"

	"github.com/justrunme/architecture-rehearsal/internal/graph"
)

// BlastRadius reports other workloads affected via causal graph edges.
type BlastRadius struct{}

func (BlastRadius) Name() string { return "blast-radius" }

func (d BlastRadius) Evaluate(ctx Context) Verdict {
	v := Verdict{Detector: d.Name(), Confidence: "medium"}
	if ctx.Change == nil || ctx.PropIdx == nil {
		v.Status = Unknown
		v.Explanation = "missing change or proposed graph index"
		v.Missing = []string{"change", "graph"}
		v.Confidence = "low"
		return v
	}

	seeds := ctx.Change.Seeds
	if len(seeds) == 0 {
		// No seeds → nothing to fan out; PASS only when change is empty of graph ops.
		if len(ctx.Change.AddedNodes) == 0 && len(ctx.Change.PatchNodes) == 0 && len(ctx.Change.RemovedNodes) == 0 {
			v.Status = Pass
			v.Explanation = "empty change; no blast radius"
			v.Confidence = "high"
			return v
		}
		v.Status = Unknown
		v.Explanation = "change has graph mutations but no seeds for blast-radius traversal"
		v.Missing = []string{"seeds"}
		v.Confidence = "low"
		return v
	}

	hasEdges := (ctx.Proposed != nil && len(ctx.Proposed.Edges) > 0) ||
		(ctx.Baseline != nil && len(ctx.Baseline.Edges) > 0)
	if !hasEdges {
		// No dependency edges to walk: blast radius is limited to the change seeds
		// themselves (common for single-workload apps). Do not fail-closed UNKNOWN
		// here — env/image template diffs are real changes with seeds but often
		// have no DEPENDS_ON/ROUTES_TO edges in the live snapshot.
		v.Status = Pass
		v.Explanation = "no dependency edges; blast radius limited to change seeds"
		v.Evidence = []string{fmt.Sprintf("seeds=%d", len(seeds))}
		v.Confidence = "medium"
		return v
	}

	allowed := map[graph.Relation]bool{
		graph.RelDependsOn:   true,
		graph.RelRoutesTo:    true,
		graph.RelObservedBy:  true,
		graph.RelProtectedBy: true,
		graph.RelBindsVolume: true,
		graph.RelScales:      true,
	}

	// Dependents of each seed + forward dependents from workloads.
	seen := map[string]bool{}
	var affected []string
	for _, seed := range seeds {
		for _, id := range graph.DependentsOf(ctx.PropIdx, seed) {
			if seen[id] || id == seed {
				continue
			}
			seen[id] = true
			affected = append(affected, id)
		}
		for _, id := range graph.WalkForward(ctx.PropIdx, []string{seed}, allowed, 6) {
			if seen[id] || id == seed {
				continue
			}
			n := ctx.PropIdx.ByID[id]
			if n == nil {
				continue
			}
			if n.Kind == graph.KindWorkload || n.Kind == graph.KindService || n.Kind == graph.KindPVC {
				seen[id] = true
				affected = append(affected, id)
			}
		}
	}

	workloadCount := 0
	var names []string
	for _, id := range affected {
		n := ctx.PropIdx.ByID[id]
		if n == nil {
			n = ctx.BaseIdx.ByID[id]
		}
		if n == nil {
			continue
		}
		if n.Kind == graph.KindWorkload {
			workloadCount++
			names = append(names, display(ctx.PropIdx, id))
		}
	}

	v.Affected = affected
	v.Evidence = []string{
		fmt.Sprintf("seeds=%d", len(seeds)),
		fmt.Sprintf("affected_nodes=%d", len(affected)),
		fmt.Sprintf("affected_workloads=%d", workloadCount),
	}
	if len(names) > 0 {
		v.Evidence = append(v.Evidence, "workloads="+fmt.Sprint(names))
	}

	switch {
	case workloadCount >= 5:
		v.Status = Block
		v.Explanation = fmt.Sprintf("large blast radius: %d other workloads reachable via graph edges", workloadCount)
		v.Confidence = "high"
	case workloadCount >= 2:
		v.Status = Warn
		v.Explanation = fmt.Sprintf("moderate blast radius: %d other workloads affected via graph edges", workloadCount)
		v.Confidence = "high"
	case workloadCount == 1:
		v.Status = Warn
		v.Explanation = "blast radius includes 1 dependent workload"
		v.Confidence = "medium"
	default:
		v.Status = Pass
		v.Explanation = "no additional workloads in blast radius beyond change seeds"
		v.Confidence = "high"
	}
	return v
}
