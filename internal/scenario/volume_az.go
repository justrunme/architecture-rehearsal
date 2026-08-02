package scenario

import (
	"fmt"

	"github.com/justrunme/architecture-rehearsal/internal/graph"
)

// VolumeAZ detects PVC zone incompatible with remaining nodes after change.
type VolumeAZ struct{}

func (VolumeAZ) Name() string { return "volume-az" }

func (VolumeAZ) Run(ctx Context) []Finding {
	ch := ctx.Change
	if ch == nil || ctx.BaseIdx == nil || ctx.PropIdx == nil {
		return nil
	}
	if ch.EffectiveKind() != "node-failure" && ch.EffectiveKind() != "node-drain" && factString(ch.Facts, "scenario", "") != "volume-az" {
		return nil
	}
	// zones still available in proposed
	zones := map[string]bool{}
	for _, n := range ctx.PropIdx.ByKind[graph.KindNode] {
		if z := n.AttrString("zone"); z != "" {
			zones[z] = true
		}
	}
	if len(zones) == 0 {
		return nil
	}
	var findings []Finding
	for _, pvc := range ctx.BaseIdx.ByKind[graph.KindPVC] {
		z := pvc.AttrString("zone")
		if z == "" {
			continue
		}
		if !zones[z] {
			findings = append(findings, Finding{
				ID:       "volume-az:" + pvc.ID,
				Scenario: "volume-az",
				Risk:     "critical",
				Title:    "PVC zone has no remaining nodes after change",
				Summary: fmt.Sprintf(
					"PVC %s is in zone %s but no schedulable nodes remain in that zone after the change.",
					pvc.Name, z,
				),
				Components: []string{pvc.ID},
				Cascade: []string{
					fmt.Sprintf("PVC zone=%s", z),
					"all nodes in zone removed or unschedulable",
					"volume cannot attach",
					"stateful pod Pending forever",
				},
				Controls:   []string{"ensure multi-AZ node capacity", "use multi-AZ capable storage where possible"},
				SLOImpact:  "availability",
				Evidence:   []string{"pvc.zone=" + z, "remaining_zones_missing"},
				Rollback:   RollbackUnknown,
				Confidence: "high",
			})
		}
	}
	return findings
}
