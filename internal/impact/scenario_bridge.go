package impact

import (
	"github.com/justrunme/architecture-rehearsal/internal/scenario"
)

// scenarioWrap adapts a Detector into a scenario.Runner for analyze.Run.
type scenarioWrap struct {
	d Detector
}

func (w scenarioWrap) Name() string { return "argo-" + w.d.Name() }

func (w scenarioWrap) Applicable(ctx scenario.Context) bool {
	ch := ctx.Change
	if ch == nil {
		return false
	}
	if ch.EffectiveKind() == "argo-sync" {
		return true
	}
	if ch.Facts == nil {
		return false
	}
	s, _ := ch.Facts["scenario"].(string)
	return s == "argo-impact"
}

func (w scenarioWrap) MissingRequirements(ctx scenario.Context) []scenario.Requirement {
	return nil
}

func (w scenarioWrap) Evaluate(ctx scenario.Context) scenario.Result {
	v := w.d.Evaluate(FromScenario(ctx))
	switch v.Status {
	case Pass:
		return scenario.Result{Outcome: scenario.OutcomeNotMatched}
	case Unknown:
		miss := make([]scenario.Requirement, 0, len(v.Missing))
		for _, m := range v.Missing {
			miss = append(miss, scenario.Requirement{ID: m, Message: v.Explanation})
		}
		if len(miss) == 0 {
			miss = []scenario.Requirement{{ID: w.Name(), Message: v.Explanation}}
		}
		return scenario.Result{
			Outcome: scenario.OutcomeUnknown,
			Missing: miss,
			Findings: []scenario.Finding{{
				ID: w.Name(), Scenario: w.Name(), Risk: "unknown",
				Title: "Insufficient data: " + w.d.Name(), Summary: v.Explanation,
				Components: v.Affected, Evidence: append(append([]string{}, v.Evidence...), v.Missing...),
				Rollback: scenario.RollbackUnknown, Confidence: v.Confidence,
			}},
		}
	default:
		risk := "medium"
		if v.Status == Block {
			risk = "high"
		}
		title := w.d.Name()
		if v.Status != "" {
			title = v.Status + ": " + w.d.Name()
		}
		return scenario.Result{
			Outcome: scenario.OutcomeMatched,
			Findings: []scenario.Finding{{
				ID: w.Name(), Scenario: w.Name(), Risk: risk,
				Title: title, Summary: v.Explanation, Components: v.Affected,
				Cascade: v.Evidence, Evidence: v.Evidence,
				Controls: []string{"review Argo sync impact report", "adjust desired manifests or cluster capacity"},
				SLOImpact: "availability", Rollback: scenario.RollbackUnknown, Confidence: v.Confidence,
			}},
		}
	}
}

// ScenarioRunners returns impact detectors as scenario.Runner values for analyze.
func ScenarioRunners() []scenario.Runner {
	var out []scenario.Runner
	for _, d := range DefaultDetectors() {
		out = append(out, scenarioWrap{d: d})
	}
	return out
}

// AllRunners is Default scenario runners plus Argo impact bridges.
func AllRunners() []scenario.Runner {
	return append(scenario.DefaultRunners(), ScenarioRunners()...)
}
