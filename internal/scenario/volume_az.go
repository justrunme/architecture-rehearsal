package scenario

import (
	"fmt"

	"github.com/justrunme/architecture-rehearsal/internal/graph"
)

type VolumeAZ struct{}

func (VolumeAZ) Name() string { return "volume-az" }

func (VolumeAZ) Applicable(ctx Context) bool {
	ch := ctx.Change
	if ch == nil {
		return false
	}
	k := changeKind(ch)
	return k == "node-failure" || k == "node-drain" || factString(ch.Facts, "scenario", "") == "volume-az"
}

func (VolumeAZ) MissingRequirements(ctx Context) []Requirement {
	if !hasKind(ctx.BaseIdx, graph.KindPVC) {
		return nil
	}
	hasZone := false
	for _, n := range ctx.BaseIdx.ByKind[graph.KindPVC] {
		if n.AttrString("zone") != "" {
			hasZone = true
			break
		}
	}
	if !hasZone {
		return []Requirement{{ID: "pvc-zone", Message: "PVC nodes lack zone attributes"}}
	}
	if !hasKind(ctx.PropIdx, graph.KindNode) {
		return []Requirement{{ID: "nodes-proposed", Message: "no Node objects in proposed graph to evaluate AZ capacity"}}
	}
	return nil
}

func (r VolumeAZ) Evaluate(ctx Context) Result {
	if !hasKind(ctx.BaseIdx, graph.KindPVC) {
		return Result{Outcome: OutcomeNotMatched}
	}
	zones := map[string]bool{}
	for _, n := range ctx.PropIdx.ByKind[graph.KindNode] {
		if z := n.AttrString("zone"); z != "" {
			zones[z] = true
		}
	}
	if len(zones) == 0 {
		return Result{Outcome: OutcomeNotMatched}
	}
	var findings []Finding
	for _, pvc := range ctx.BaseIdx.ByKind[graph.KindPVC] {
		z := pvc.AttrString("zone")
		if z == "" || zones[z] {
			continue
		}
		findings = append(findings, Finding{
			ID: "volume-az:" + pvc.ID, Scenario: r.Name(), Risk: "critical",
			Title: "PVC zone has no remaining nodes after change",
			Summary: fmt.Sprintf("PVC %s zone=%s but no nodes remain in that zone.", pvc.Name, z),
			Components: []string{pvc.ID},
			Cascade: []string{fmt.Sprintf("PVC zone=%s", z), "no nodes in zone", "volume cannot attach"},
			Controls: []string{"ensure multi-AZ node capacity", "multi-AZ storage where possible"},
			SLOImpact: "availability", Evidence: []string{"pvc.zone=" + z},
			Rollback: RollbackUnknown, Confidence: "high",
		})
	}
	if len(findings) == 0 {
		return Result{Outcome: OutcomeNotMatched}
	}
	return Result{Outcome: OutcomeMatched, Findings: findings}
}
