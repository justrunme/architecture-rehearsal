// Package verify compares an analysis prediction with a post-deploy snapshot.
package verify

import (
	"fmt"
	"time"

	"github.com/justrunme/architecture-rehearsal/internal/analyze"
	"github.com/justrunme/architecture-rehearsal/internal/graph"
)

// Outcome of verification.
const (
	OutcomeVerified     = "verified"
	OutcomeDiverged     = "diverged"
	OutcomeInconclusive = "inconclusive"
)

// Result is the verify report.
type Result struct {
	APIVersion     string    `json:"apiVersion"`
	Kind           string    `json:"kind"`
	Version        string    `json:"version"`
	Generated      time.Time `json:"generatedAt"`
	ChangeID       string    `json:"changeId"`
	PredictionRisk string    `json:"predictionRisk"`
	Outcome        string    `json:"outcome"`
	Summary        string    `json:"summary"`
	Checks         []Check   `json:"checks"`
	Score          float64   `json:"score"` // 0..1 fraction of checks that matched
}

// Check is one verification assertion.
type Check struct {
	Name    string `json:"name"`
	Passed  bool   `json:"passed"`
	Detail  string `json:"detail"`
	Unknown bool   `json:"unknown,omitempty"`
}

// Run compares prediction report to post-deploy observed snapshot.
func Run(pred *analyze.Report, observed *graph.Snapshot) *Result {
	res := &Result{
		APIVersion:     graph.APIVersionV1Alpha1,
		Kind:           "VerificationResult",
		Version:        analyze.Version,
		Generated:      time.Now().UTC(),
		ChangeID:       pred.ChangeID,
		PredictionRisk: pred.Risk,
	}
	idx := graph.BuildIndex(observed)
	var checks []Check

	// 1. Predicted failure scenarios — soft check via meta.observed_failures if present
	observedFailures := map[string]bool{}
	if observed.Meta != nil {
		if raw, ok := observed.Meta["observed_failures"].([]any); ok {
			for _, x := range raw {
				if s, ok := x.(string); ok {
					observedFailures[s] = true
				}
			}
		}
	}
	if len(pred.PredictedFailures) == 0 {
		checks = append(checks, Check{
			Name:   "no_predicted_failures",
			Passed: true,
			Detail: "analysis predicted no failure patterns",
		})
	} else if len(observedFailures) == 0 {
		checks = append(checks, Check{
			Name:    "predicted_failures_observed",
			Passed:  false,
			Unknown: true,
			Detail:  "prediction listed failures but observed snapshot has no meta.observed_failures — inconclusive",
		})
	} else {
		matched := 0
		for _, f := range pred.PredictedFailures {
			if observedFailures[f] {
				matched++
			}
		}
		checks = append(checks, Check{
			Name:   "predicted_failures_observed",
			Passed: matched > 0,
			Detail: fmt.Sprintf("matched %d/%d predicted failure ids in observed meta", matched, len(pred.PredictedFailures)),
		})
	}

	// 2. Workloads still present that were not removed
	for _, f := range pred.Findings {
		for _, c := range f.Components {
			if n := idx.ByID[c]; n != nil {
				checks = append(checks, Check{
					Name:   "component_present:" + c,
					Passed: true,
					Detail: "component still in observed graph",
				})
			}
		}
	}

	// 3. If RWO predicted critical and observed has pending stateful pods
	if contains(pred.PredictedFailures, "rwo-node-loss") {
		pending := 0
		for _, w := range idx.ByKind[graph.KindWorkload] {
			if w.AttrString("phase") == "Pending" || w.AttrBool("unschedulable") {
				pending++
			}
		}
		if pending > 0 {
			checks = append(checks, Check{Name: "rwo_pending_pods", Passed: true, Detail: fmt.Sprintf("%d pending/unschedulable workloads observed", pending)})
		} else if len(observedFailures) == 0 {
			checks = append(checks, Check{Name: "rwo_pending_pods", Passed: false, Unknown: true, Detail: "no pending workloads recorded — may have recovered or evidence missing"})
		}
	}

	passed, total, unknowns := 0, 0, 0
	for _, c := range checks {
		if c.Unknown {
			unknowns++
			continue
		}
		total++
		if c.Passed {
			passed++
		}
	}
	res.Checks = checks
	if total > 0 {
		res.Score = float64(passed) / float64(total)
	}
	switch {
	case unknowns > 0 && passed == total:
		res.Outcome = OutcomeInconclusive
		res.Summary = "Some checks lack post-deploy evidence; result inconclusive."
	case total > 0 && passed == total:
		res.Outcome = OutcomeVerified
		res.Summary = "Observed state is consistent with analysis prediction."
	case total > 0 && passed == 0:
		res.Outcome = OutcomeDiverged
		res.Summary = "Observed state diverged from prediction."
	default:
		res.Outcome = OutcomeDiverged
		res.Summary = fmt.Sprintf("Partial match: %d/%d checks passed.", passed, total)
	}
	return res
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}
