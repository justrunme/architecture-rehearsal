package scenario

import (
	"fmt"

	"github.com/justrunme/architecture-rehearsal/internal/graph"
)

type RWONodeLoss struct{}

func (RWONodeLoss) Name() string { return "rwo-node-loss" }

func (RWONodeLoss) Applicable(ctx Context) bool {
	ch := ctx.Change
	if ch == nil {
		return false
	}
	k := changeKind(ch)
	if k == "node-failure" || k == "node-drain" {
		return true
	}
	return factString(ch.Facts, "scenario", "") == "rwo-node-loss" || factString(ch.Facts, "event", "") == "node_loss"
}

func (RWONodeLoss) MissingRequirements(ctx Context) []Requirement {
	var miss []Requirement
	if !hasKind(ctx.BaseIdx, graph.KindNode) {
		miss = append(miss, Requirement{ID: "node", Message: "no Node objects in baseline"})
	}
	if !hasKind(ctx.BaseIdx, graph.KindPVC) {
		miss = append(miss, Requirement{ID: "pvc", Message: "no PVC objects — cannot evaluate RWO reattach"})
	}
	// need at least one BINDS_VOLUME edge or volumeClaims attr
	hasVolLink := false
	if ctx.BaseIdx != nil {
		for _, e := range ctx.Baseline.Edges {
			if e.Rel == graph.RelBindsVolume {
				hasVolLink = true
				break
			}
		}
		for _, n := range ctx.BaseIdx.ByKind[graph.KindWorkload] {
			if n.Attributes["volumeClaims"] != nil {
				hasVolLink = true
			}
		}
	}
	if !hasVolLink && hasKind(ctx.BaseIdx, graph.KindPVC) {
		miss = append(miss, Requirement{ID: "workload-pvc-edge", Message: "no Workload→PVC edges or volumeClaims attributes"})
	}
	return miss
}

func (r RWONodeLoss) Evaluate(ctx Context) Result {
	ch := ctx.Change
	idx := ctx.BaseIdx
	lostNode := factString(ch.Facts, "lost_node", "")
	if lostNode == "" {
		for _, id := range ch.RemovedNodes {
			if n := idx.ByID[id]; n != nil && n.Kind == graph.KindNode {
				lostNode = id
				break
			}
		}
	}
	if lostNode == "" {
		return Result{Outcome: OutcomeNotMatched}
	}

	var findings []Finding
	for _, pvc := range idx.ByKind[graph.KindPVC] {
		access := pvc.AttrString("accessMode")
		if access != "ReadWriteOnce" && access != "RWO" {
			continue
		}
		boundNode := pvc.AttrString("boundNode")
		lostName := ""
		if n := idx.ByID[lostNode]; n != nil {
			lostName = n.Name
		}
		if boundNode != lostNode && boundNode != lostName {
			continue
		}
		workloads := workloadsForPVC(idx, pvc.ID)
		comps := append([]string{pvc.ID, lostNode}, workloads...)
		cascade := []string{
			fmt.Sprintf("node %s unavailable", display(idx, lostNode)),
			fmt.Sprintf("RWO PVC %s still attached to lost node", pvc.Name),
			"volume cannot attach to replacement node until detach completes",
		}
		for _, w := range workloads {
			cascade = append(cascade, fmt.Sprintf("workload %s cannot schedule", display(idx, w)))
			for _, dep := range graph.DependentsOf(idx, w) {
				if n := idx.ByID[dep]; n != nil {
					cascade = append(cascade, fmt.Sprintf("dependent %s %s impacted", n.Kind, n.Name))
					comps = append(comps, dep)
				}
			}
		}
		capacityOK := factBool(ch.Facts, "replacement_capacity_available", false)
		zoneOK := factBool(ch.Facts, "replacement_zone_compatible", true)
		if !capacityOK {
			cascade = append(cascade, "insufficient free capacity for replacement pod")
		}
		risk := "critical"
		if capacityOK && zoneOK {
			risk = "high"
		}
		rb := RollbackUnknown
		if ch.Facts != nil {
			if v, ok := ch.Facts["rollback_available"].(bool); ok {
				if v {
					rb = RollbackAvailable
				} else {
					rb = RollbackUnavailable
				}
			}
		}
		findings = append(findings, Finding{
			ID: "rwo-node-loss:" + pvc.ID, Scenario: r.Name(), Risk: risk,
			Title: "Stateful service unavailable after node loss (RWO volume)",
			Summary: fmt.Sprintf("PVC %s (RWO) bound to %s cannot reattach after node loss.", pvc.Name, display(idx, lostNode)),
			Components: unique(comps), Cascade: cascade,
			Controls: []string{"validate detach/attach path", "reserve same-AZ capacity", "prepare rollback window"},
			SLOImpact: "availability SLO at risk", Evidence: []string{"accessMode=RWO", "boundNode=" + boundNode},
			Rollback: rb, Confidence: "high",
		})
	}
	if len(findings) == 0 {
		return Result{Outcome: OutcomeNotMatched}
	}
	return Result{Outcome: OutcomeMatched, Findings: findings}
}

func workloadsForPVC(idx *graph.Index, pvcID string) []string {
	var out []string
	for id, edges := range idx.Out {
		for _, e := range edges {
			if e.To == pvcID && (e.Rel == graph.RelBindsVolume || e.Rel == graph.RelDependsOn) {
				if n := idx.ByID[id]; n != nil && n.Kind == graph.KindWorkload {
					out = append(out, n.ID)
				}
			}
		}
	}
	return unique(out)
}
