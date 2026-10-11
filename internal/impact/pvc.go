package impact

import (
	"fmt"

	"github.com/justrunme/architecture-rehearsal/internal/graph"
)

// PVCZoneConflict detects PVC vs node affinity / storage class zone conflicts.
type PVCZoneConflict struct{}

func (PVCZoneConflict) Name() string { return "pvc-zone-conflict" }

func (d PVCZoneConflict) Evaluate(ctx Context) Verdict {
	v := Verdict{Detector: d.Name(), Confidence: "medium"}
	if ctx.BaseIdx == nil || ctx.PropIdx == nil {
		v.Status = Unknown
		v.Explanation = "missing graph indexes"
		v.Missing = []string{"graph"}
		v.Confidence = "low"
		return v
	}

	pvcs := ctx.PropIdx.ByKind[graph.KindPVC]
	if len(pvcs) == 0 {
		// Check if change touches PVCs without baseline coverage.
		touched := false
		if ctx.Change != nil {
			for _, id := range ctx.Change.Seeds {
				if len(id) >= 4 && id[:4] == "pvc/" {
					touched = true
					break
				}
			}
			for _, n := range ctx.Change.AddedNodes {
				if n.Kind == graph.KindPVC {
					touched = true
				}
			}
		}
		if touched {
			v.Status = Unknown
			v.Explanation = "change references PVCs but proposed graph has no PVC nodes"
			v.Missing = []string{"pvc"}
			v.Confidence = "low"
			return v
		}
		v.Status = Pass
		v.Explanation = "no PVCs in proposed graph"
		v.Confidence = "high"
		return v
	}

	nodes := ctx.PropIdx.ByKind[graph.KindNode]
	if len(nodes) == 0 {
		nodes = ctx.BaseIdx.ByKind[graph.KindNode]
	}

	needsZone := false
	for _, pvc := range pvcs {
		if pvc.AttrString("zone") != "" || pvc.AttrString("storageClass") != "" {
			needsZone = true
			break
		}
		// Bound workloads with node affinity imply zone sensitivity.
		for _, e := range ctx.PropIdx.In[pvc.ID] {
			if e.Rel == graph.RelBindsVolume {
				if w := ctx.PropIdx.ByID[e.From]; w != nil && (w.AttrBool("hasAffinity") || w.Attributes["nodeSelector"] != nil) {
					needsZone = true
				}
			}
		}
	}

	zoneSet := map[string]bool{}
	for _, n := range nodes {
		if z := n.AttrString("zone"); z != "" {
			zoneSet[z] = true
		}
	}

	if needsZone && len(nodes) == 0 {
		v.Status = Unknown
		v.Explanation = "PVC/storage constraints present but no Node objects to evaluate zones"
		v.Missing = []string{"nodes"}
		v.Confidence = "low"
		return v
	}

	var conflicts []string
	var affected []string
	for _, pvc := range pvcs {
		pz := pvc.AttrString("zone")
		sc := pvc.AttrString("storageClass")

		// Workload nodeSelector / affinity zone must intersect PVC zone.
		for _, e := range ctx.PropIdx.In[pvc.ID] {
			if e.Rel != graph.RelBindsVolume {
				continue
			}
			w := ctx.PropIdx.ByID[e.From]
			if w == nil {
				continue
			}
			if sel, ok := w.Attributes["nodeSelector"].(map[string]any); ok {
				wz := fmt.Sprint(sel["topology.kubernetes.io/zone"])
				if wz == "" {
					wz = fmt.Sprint(sel["failure-domain.beta.kubernetes.io/zone"])
				}
				if pz != "" && wz != "" && pz != wz {
					conflicts = append(conflicts, fmt.Sprintf(
						"PVC %s zone=%s conflicts with workload %s nodeSelector zone=%s",
						pvc.Name, pz, display(ctx.PropIdx, w.ID), wz))
					affected = append(affected, pvc.ID, w.ID)
				}
			}
		}

		if pz != "" && len(zoneSet) > 0 && !zoneSet[pz] {
			conflicts = append(conflicts, fmt.Sprintf(
				"PVC %s zone=%s has no schedulable nodes in that zone (storageClass=%s)",
				pvc.Name, pz, sc))
			affected = append(affected, pvc.ID)
		}

		// storage class zonal: if meta marks SC as zonal and PVC has no zone → unknown
		if sc != "" && pz == "" && ctx.Baseline != nil && ctx.Baseline.Meta != nil {
			if zonal, ok := ctx.Baseline.Meta["storageClassZonal"].(map[string]any); ok {
				if fmt.Sprint(zonal[sc]) == "true" || zonal[sc] == true {
					v.Status = Unknown
					v.Explanation = fmt.Sprintf("zonal storageClass %s on PVC %s without zone attribute", sc, pvc.Name)
					v.Missing = []string{"pvc.zone"}
					v.Confidence = "low"
					return v
				}
			}
		}
	}

	if len(conflicts) > 0 {
		v.Status = Block
		v.Explanation = "PVC zone / node affinity conflict would prevent volume attach"
		v.Evidence = conflicts
		v.Affected = unique(affected)
		v.Confidence = "high"
		return v
	}

	// PVCs exist with zones but nodes lack zones → cannot prove safety
	anyPVCZone := false
	for _, pvc := range pvcs {
		if pvc.AttrString("zone") != "" {
			anyPVCZone = true
			break
		}
	}
	if anyPVCZone && len(zoneSet) == 0 {
		v.Status = Unknown
		v.Explanation = "PVCs declare zones but nodes lack zone labels"
		v.Missing = []string{"node.zone"}
		v.Confidence = "low"
		return v
	}

	v.Status = Pass
	v.Explanation = "no PVC / node affinity zone conflicts detected"
	v.Confidence = "high"
	return v
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
