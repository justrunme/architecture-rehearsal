package scenario

import (
	"fmt"

	"github.com/justrunme/architecture-rehearsal/internal/graph"
)

type ServiceRouting struct{}

func (ServiceRouting) Name() string { return "service-routing" }

func (ServiceRouting) Applicable(ctx Context) bool {
	ch := ctx.Change
	if ch == nil {
		return false
	}
	if len(ch.RemovedNodes) > 0 {
		return true
	}
	return factString(ch.Facts, "scenario", "") == "service-routing"
}

func (ServiceRouting) MissingRequirements(ctx Context) []Requirement {
	if !hasKind(ctx.BaseIdx, graph.KindService) {
		return nil
	}
	hasRoute := false
	if ctx.Baseline != nil {
		for _, e := range ctx.Baseline.Edges {
			if e.Rel == graph.RelRoutesTo || e.Rel == graph.RelDependsOn {
				// check if from is service
				if n := ctx.BaseIdx.ByID[e.From]; n != nil && n.Kind == graph.KindService {
					hasRoute = true
					break
				}
			}
		}
	}
	if !hasRoute {
		return []Requirement{{ID: "service-edges", Message: "Service nodes exist but no ROUTES_TO edges to workloads"}}
	}
	return nil
}

func (r ServiceRouting) Evaluate(ctx Context) Result {
	if !hasKind(ctx.BaseIdx, graph.KindService) {
		return Result{Outcome: OutcomeNotMatched}
	}
	removed := map[string]bool{}
	for _, id := range ctx.Change.RemovedNodes {
		removed[id] = true
	}
	var findings []Finding
	for _, svc := range ctx.BaseIdx.ByKind[graph.KindService] {
		var backends []string
		for _, e := range ctx.BaseIdx.Out[svc.ID] {
			if e.Rel == graph.RelRoutesTo || e.Rel == graph.RelDependsOn {
				backends = append(backends, e.To)
			}
		}
		if len(backends) == 0 {
			continue
		}
		alive := 0
		for _, b := range backends {
			if removed[b] {
				continue
			}
			if ctx.PropIdx.ByID[b] != nil {
				alive++
			}
		}
		if alive == 0 {
			findings = append(findings, Finding{
				ID: "service-routing:" + svc.ID, Scenario: r.Name(), Risk: "critical",
				Title: "Service has no remaining backends after change",
				Summary: fmt.Sprintf("Service %s backends are all removed.", display(ctx.BaseIdx, svc.ID)),
				Components: append([]string{svc.ID}, backends...),
				Cascade: []string{"backends removed", "endpoints empty", "callers fail"},
				Controls: []string{"update selector before remove", "progressive cutover"},
				SLOImpact: "availability", Evidence: []string{"alive_backends=0"},
				Rollback: RollbackAvailable, Confidence: "high",
			})
		}
	}
	if len(findings) == 0 {
		return Result{Outcome: OutcomeNotMatched}
	}
	return Result{Outcome: OutcomeMatched, Findings: findings}
}
