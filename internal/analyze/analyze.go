package analyze

import (
	"fmt"
	"strings"
	"time"

	"github.com/justrunme/architecture-rehearsal/internal/graph"
	"github.com/justrunme/architecture-rehearsal/internal/loader"
	"github.com/justrunme/architecture-rehearsal/internal/scenario"
)

const Version = "0.1.0"

// Run builds a proposed graph, executes deterministic scenarios, returns a Report.
func Run(base *graph.Snapshot, ch *loader.ChangeEnvelope) *Report {
	proposed := loader.ApplyChange(base, ch)
	baseIdx := graph.BuildIndex(base)
	propIdx := graph.BuildIndex(proposed)

	ctx := scenario.Context{
		Baseline: base,
		Proposed: proposed,
		Change:   ch,
		BaseIdx:  baseIdx,
		PropIdx:  propIdx,
	}
	findings := scenario.RunAll(ctx, scenario.DefaultRunners())

	rep := &Report{
		Version:            Version,
		Generated:          time.Now().UTC(),
		ChangeID:           ch.ID,
		ChangeTitle:        ch.Title,
		ChangeKind:         ch.Kind,
		BaselineID:         base.ID,
		Findings:           findings,
		RollbackAvailable:  true,
		PredictedFailures:  []string{},
		CoverageGaps:       coverageGaps(base),
	}

	risk := RiskNone
	sloViolations := 0
	criticalPaths := 0
	affected := map[string]bool{}
	var cascades [][]string

	for _, f := range findings {
		risk = MaxRisk(risk, f.Risk)
		if !f.RollbackOK {
			rep.RollbackAvailable = false
		}
		if f.SLOImpact != "" {
			sloViolations++
		}
		if f.Risk == RiskCritical || f.Risk == RiskHigh {
			criticalPaths++
		}
		for _, c := range f.Components {
			affected[c] = true
		}
		if len(f.Cascade) > 0 {
			cascades = append(cascades, f.Cascade)
		}
		// failure ids
		rep.PredictedFailures = append(rep.PredictedFailures, f.Scenario)
	}
	rep.Risk = risk
	rep.Decision = DecisionFromRisk(risk)
	rep.AffectedComponents = len(affected)
	rep.CriticalPaths = criticalPaths
	rep.SLOViolations = sloViolations
	rep.Cascades = cascades
	rep.PredictedFailures = uniqueStrings(rep.PredictedFailures)
	rep.Summary = summarize(rep, ch)

	return rep
}

func summarize(r *Report, ch *loader.ChangeEnvelope) string {
	if len(r.Findings) == 0 {
		return fmt.Sprintf("No deterministic risk patterns matched for change %q. Graph coverage may be incomplete — see coverage_gaps.", ch.Title)
	}
	var parts []string
	parts = append(parts, fmt.Sprintf("%d finding(s), risk=%s, decision=%s", len(r.Findings), r.Risk, r.Decision))
	for _, f := range r.Findings {
		parts = append(parts, f.Title)
	}
	return strings.Join(parts, " · ")
}

func coverageGaps(base *graph.Snapshot) []string {
	var gaps []string
	if base == nil {
		return []string{"no baseline snapshot"}
	}
	kinds := map[graph.Kind]bool{}
	for _, n := range base.Nodes {
		kinds[n.Kind] = true
	}
	if !kinds[graph.KindNode] {
		gaps = append(gaps, "no Node objects — capacity/topology scenarios limited")
	}
	if !kinds[graph.KindPVC] {
		gaps = append(gaps, "no PVC objects — RWO/node-loss path not fully modeled unless fixtures include them")
	}
	if base.Meta == nil || base.Meta["metric_labels"] == nil {
		gaps = append(gaps, "no Prometheus label schema in meta.metric_labels — prom-zero-match needs that snapshot")
	}
	if len(gaps) == 0 {
		gaps = append(gaps, "partial graph: IAM, multi-cluster, and cross-region edges not modeled in v0.1")
	} else {
		gaps = append(gaps, "partial graph: IAM, multi-cluster, and cross-region edges not modeled in v0.1")
	}
	return gaps
}

func uniqueStrings(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}
