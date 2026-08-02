package scenario

import (
	"fmt"

	"github.com/justrunme/architecture-rehearsal/internal/graph"
)

// RWONodeLoss detects stateful workloads that cannot re-attach RWO volumes after node loss.
type RWONodeLoss struct{}

func (RWONodeLoss) Name() string { return "rwo-node-loss" }

func (RWONodeLoss) Run(ctx Context) []Finding {
	ch := ctx.Change
	if ch == nil || ctx.BaseIdx == nil {
		return nil
	}
	kind := ch.EffectiveKind()
	event := factString(ch.Facts, "event", "")
	if kind != "node-failure" && kind != "node-drain" && event != "node_loss" && event != "node_drain" {
		if factString(ch.Facts, "scenario", "") != "rwo-node-loss" {
			return nil
		}
	}

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
		return nil
	}

	var findings []Finding
	for _, pvc := range idx.ByKind[graph.KindPVC] {
		access := pvc.AttrString("accessMode")
		if access != "ReadWriteOnce" && access != "RWO" {
			continue
		}
		boundNode := pvc.AttrString("boundNode")
		lostName := display(idx, lostNode)
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
			cascade = append(cascade, fmt.Sprintf("workload %s cannot schedule / stays Pending", display(idx, w)))
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
		if !zoneOK {
			cascade = append(cascade, "replacement node zone incompatible with volume AZ")
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
			ID:         "rwo-node-loss:" + pvc.ID,
			Scenario:   "rwo-node-loss",
			Risk:       risk,
			Title:      "Stateful service unavailable after node loss (RWO volume)",
			Summary:    fmt.Sprintf("PVC %s (RWO) is bound to node %s. After node loss the pod cannot reattach until detach finishes.", pvc.Name, display(idx, lostNode)),
			Components: unique(comps),
			Cascade:    cascade,
			Controls: []string{
				"validate volume detach/attach path before drain",
				"reserve replacement capacity in the same AZ",
				"verify volume zone compatibility",
				"prepare rollback / maintenance window",
			},
			SLOImpact:  "availability SLO at risk for stateful path",
			Evidence:   []string{"pvc.accessMode=RWO", "pvc.boundNode=" + boundNode, "event=node_loss"},
			Rollback:   rb,
			Confidence: "high",
		})
	}
	return findings
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
