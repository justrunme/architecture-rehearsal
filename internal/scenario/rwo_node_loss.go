package scenario

import (
	"fmt"

	"github.com/justrunme/architecture-rehearsal/internal/graph"
)

// RWONodeLoss detects stateful workloads that cannot re-attach RWO volumes
// after node failure / drain / replacement.
type RWONodeLoss struct{}

func (RWONodeLoss) Name() string { return "rwo-node-loss" }

func (RWONodeLoss) Run(ctx Context) []Finding {
	ch := ctx.Change
	if ch == nil {
		return nil
	}
	// Trigger when change removes/drains a node or marks node loss.
	kind := ch.Kind
	event := loaderFactString(ch.Facts, "event", "")
	if kind != "node-failure" && kind != "node-drain" && event != "node_loss" && event != "node_drain" {
		// Also run if change facts explicitly request this scenario.
		if loaderFactString(ch.Facts, "scenario", "") != "rwo-node-loss" {
			return nil
		}
	}

	idx := ctx.BaseIdx
	if idx == nil {
		return nil
	}

	lostNode := loaderFactString(ch.Facts, "lost_node", "")
	if lostNode == "" && len(ch.RemovedNodes) > 0 {
		// Prefer first removed node of kind Node.
		for _, id := range ch.RemovedNodes {
			if n := idx.ByID[id]; n != nil && n.Kind == graph.KindNode {
				lostNode = id
				break
			}
		}
		if lostNode == "" {
			lostNode = ch.RemovedNodes[0]
		}
	}
	if lostNode == "" {
		return nil
	}

	var findings []Finding
	// Find PVCs bound to the lost node and workloads using them.
	for _, pvc := range idx.ByKind[graph.KindPVC] {
		access := pvc.AttrString("accessMode")
		if access != "ReadWriteOnce" && access != "RWO" {
			continue
		}
		boundNode := pvc.AttrString("boundNode")
		if boundNode != lostNode && boundNode != nodeName(idx, lostNode) {
			continue
		}
		// Workloads that BINDS_VOLUME this PVC or DEPENDS_ON it.
		workloads := workloadsForPVC(idx, pvc.ID)
		comps := append([]string{pvc.ID, lostNode}, workloads...)
		// Dependents of those workloads.
		var cascade []string
		cascade = append(cascade,
			fmt.Sprintf("node %s unavailable", display(idx, lostNode)),
			fmt.Sprintf("RWO PVC %s still attached to lost node", pvc.Name),
			"volume cannot attach to replacement node until detach completes",
		)
		for _, w := range workloads {
			cascade = append(cascade, fmt.Sprintf("workload %s cannot schedule / stays Pending", display(idx, w)))
			for _, dep := range graph.DependentsOf(idx, w) {
				if n := idx.ByID[dep]; n != nil {
					cascade = append(cascade, fmt.Sprintf("dependent %s %s impacted", n.Kind, n.Name))
					comps = append(comps, dep)
				}
			}
		}
		// Capacity / zone facts
		zoneOK := loaderFactBool(ch.Facts, "replacement_zone_compatible", true)
		capacityOK := loaderFactBool(ch.Facts, "replacement_capacity_available", false)
		if !capacityOK {
			cascade = append(cascade, "insufficient free capacity for replacement pod")
		}
		if !zoneOK {
			cascade = append(cascade, "replacement node zone incompatible with volume AZ")
		}

		risk := "critical"
		if capacityOK && zoneOK {
			risk = "high" // still RWO reattach delay
		}

		findings = append(findings, Finding{
			ID:       "rwo-node-loss:" + pvc.ID,
			Scenario: "rwo-node-loss",
			Risk:     risk,
			Title:    "Stateful service unavailable after node loss (RWO volume)",
			Summary: fmt.Sprintf(
				"PVC %s (RWO) is bound to node %s. After node loss the pod cannot reattach until detach finishes; dependents become unavailable.",
				pvc.Name, display(idx, lostNode),
			),
			Components: unique(comps),
			Cascade:    cascade,
			Controls: []string{
				"validate volume detach/attach path before drain",
				"reserve replacement capacity in the same AZ",
				"verify volume zone compatibility",
				"prepare rollback / maintenance window",
				"consider ReadWriteMany or multi-AZ capable storage for critical paths",
			},
			SLOImpact:  "availability SLO at risk for stateful path",
			Evidence:   []string{"pvc.accessMode=RWO", "pvc.boundNode=" + boundNode, "event=node_loss"},
			RollbackOK: loaderFactBool(ch.Facts, "rollback_available", true),
			Confidence: "high",
		})
	}
	return findings
}

func workloadsForPVC(idx *graph.Index, pvcID string) []string {
	var out []string
	for _, e := range idx.In[pvcID] {
		if e.Rel == graph.RelBindsVolume || e.Rel == graph.RelDependsOn {
			if n := idx.ByID[e.From]; n != nil && n.Kind == graph.KindWorkload {
				out = append(out, n.ID)
			}
		}
	}
	// Also: workloads with edge BINDS_VOLUME out to pvc
	for _, e := range idx.Out {
		_ = e
	}
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

func nodeName(idx *graph.Index, id string) string {
	if n := idx.ByID[id]; n != nil {
		return n.Name
	}
	return id
}

func display(idx *graph.Index, id string) string {
	if n := idx.ByID[id]; n != nil {
		if n.Namespace != "" {
			return n.Namespace + "/" + n.Name
		}
		return n.Name
	}
	return id
}

func unique(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

func loaderFactString(m map[string]any, key, def string) string {
	if m == nil {
		return def
	}
	if v, ok := m[key].(string); ok && v != "" {
		return v
	}
	return def
}

func loaderFactBool(m map[string]any, key string, def bool) bool {
	if m == nil {
		return def
	}
	if v, ok := m[key].(bool); ok {
		return v
	}
	return def
}
