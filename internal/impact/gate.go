package impact

import (
	"fmt"
	"sort"

	"github.com/justrunme/architecture-rehearsal/internal/graph"
	"github.com/justrunme/architecture-rehearsal/internal/loader"
)

// Run evaluates all detectors and aggregates a Report.
func Run(base *graph.Snapshot, ch *loader.ChangeEnvelope, detectors []Detector) *Report {
	if detectors == nil {
		detectors = DefaultDetectors()
	}
	proposed := loader.ApplyChange(base, ch)
	ctx := Context{
		Baseline:      base,
		Proposed:      proposed,
		Change:        ch,
		BaseIdx:       graph.BuildIndex(base),
		PropIdx:       graph.BuildIndex(proposed),
		PDBsCollected: pdbsInventoryKnown(base),
	}

	var verdicts []Verdict
	for _, d := range detectors {
		verdicts = append(verdicts, d.Evaluate(ctx))
	}
	sort.SliceStable(verdicts, func(i, j int) bool {
		return verdicts[i].Detector < verdicts[j].Detector
	})

	gate := Aggregate(verdicts)
	rep := &Report{
		ChangeID:   ch.ID,
		Verdict:    gate,
		Detectors:  verdicts,
		Confidence: aggregateConfidence(verdicts),
	}
	if ch.Facts != nil {
		if a, ok := ch.Facts["argo.app"].(string); ok {
			rep.AppName = a
		}
		if r, ok := ch.Facts["argo.revision"].(string); ok {
			rep.Revision = r
		}
	}

	affected := map[string]bool{}
	missing := map[string]bool{}
	var predicted []string
	for _, v := range verdicts {
		for _, a := range v.Affected {
			affected[a] = true
		}
		for _, m := range v.Missing {
			missing[m] = true
		}
		if v.Status == Block || v.Status == Warn {
			predicted = append(predicted, fmt.Sprintf("[%s] %s", v.Status, v.Explanation))
		}
		if v.Status == Unknown {
			predicted = append(predicted, fmt.Sprintf("[UNKNOWN] %s: %s", v.Detector, v.Explanation))
		}
	}
	for a := range affected {
		rep.Affected = append(rep.Affected, a)
	}
	sort.Strings(rep.Affected)
	for m := range missing {
		rep.Missing = append(rep.Missing, m)
	}
	sort.Strings(rep.Missing)
	rep.Predicted = predicted
	rep.Summary = summaryLine(gate, verdicts)
	return rep
}

// MergeAnalyze folds an analyze decision into the impact report gate verdict.
func MergeAnalyze(rep *Report, decision, risk string) {
	if rep == nil {
		return
	}
	rep.AnalyzeDecision = decision
	rep.AnalyzeRisk = risk
	rep.Verdict = mergeVerdicts(rep.Verdict, decision)
	rep.Summary = summaryLine(rep.Verdict, rep.Detectors)
}

func mergeVerdicts(impactStatus, analyzeDecision string) string {
	mapped := map[string]string{
		"approve": Pass,
		"warn":    Warn,
		"block":   Block,
		"unknown": Unknown,
	}
	a := mapped[analyzeDecision]
	if a == "" {
		a = Unknown
	}
	return Aggregate([]Verdict{{Status: impactStatus}, {Status: a}})
}

func aggregateConfidence(vs []Verdict) string {
	rank := map[string]int{"high": 3, "medium": 2, "low": 1}
	best := 3
	any := false
	for _, v := range vs {
		if v.Status == Pass {
			continue
		}
		any = true
		if rank[v.Confidence] < best {
			best = rank[v.Confidence]
		}
	}
	if !any {
		return "high"
	}
	for k, r := range rank {
		if r == best {
			return k
		}
	}
	return "medium"
}

func summaryLine(gate string, vs []Verdict) string {
	counts := map[string]int{}
	for _, v := range vs {
		counts[v.Status]++
	}
	return fmt.Sprintf("gate=%s detectors: PASS=%d WARN=%d BLOCK=%d UNKNOWN=%d",
		gate, counts[Pass], counts[Warn], counts[Block], counts[Unknown])
}

func pdbsInventoryKnown(base *graph.Snapshot) bool {
	if base == nil {
		return false
	}
	if base.Meta != nil {
		switch v := base.Meta["pdbsCollected"].(type) {
		case bool:
			return v
		case string:
			return v == "true" || v == "1"
		}
	}
	for _, n := range base.Nodes {
		if n.Kind == graph.KindPDB {
			return true
		}
	}
	return false
}
