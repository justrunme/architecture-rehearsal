package scenario

import (
	"fmt"

	"github.com/justrunme/architecture-rehearsal/internal/graph"
)

// ServiceRouting detects selector/service breaks when workloads are removed or renamed.
type ServiceRouting struct{}

func (ServiceRouting) Name() string { return "service-routing" }

func (ServiceRouting) Run(ctx Context) []Finding {
	ch := ctx.Change
	if ch == nil || ctx.BaseIdx == nil || ctx.PropIdx == nil {
		return nil
	}
	if len(ch.RemovedNodes) == 0 && factString(ch.Facts, "scenario", "") != "service-routing" {
		// also check selector patches
		hasSelectorPatch := false
		for _, p := range ch.PatchNodes {
			if _, ok := p.Attributes["selector"]; ok {
				hasSelectorPatch = true
			}
		}
		if !hasSelectorPatch {
			return nil
		}
	}
	var findings []Finding
	removed := map[string]bool{}
	for _, id := range ch.RemovedNodes {
		removed[id] = true
	}
	for _, svc := range ctx.BaseIdx.ByKind[graph.KindService] {
		// Workloads this service routes to
		var backends []string
		for _, e := range ctx.BaseIdx.Out[svc.ID] {
			if e.Rel == graph.RelRoutesTo || e.Rel == graph.RelDependsOn {
				backends = append(backends, e.To)
			}
		}
		// reverse: workload edges rarely point from service
		alive := 0
		for _, b := range backends {
			if removed[b] {
				continue
			}
			if ctx.PropIdx.ByID[b] != nil {
				alive++
			}
		}
		if len(backends) > 0 && alive == 0 {
			findings = append(findings, Finding{
				ID:       "service-routing:" + svc.ID,
				Scenario: "service-routing",
				Risk:     "critical",
				Title:    "Service has no remaining backends after change",
				Summary:  fmt.Sprintf("Service %s routes to workloads that are all removed by this change.", display(ctx.BaseIdx, svc.ID)),
				Components: append([]string{svc.ID}, backends...),
				Cascade: []string{
					"workload(s) removed or unselected",
					"Service endpoints empty",
					"dependent callers receive connection errors",
				},
				Controls:   []string{"update Service selector before removing backends", "use progressive delivery / dual-write cutover"},
				SLOImpact:  "availability",
				Evidence:   []string{"alive_backends=0"},
				Rollback:   RollbackAvailable,
				Confidence: "high",
			})
		}
	}
	return findings
}
