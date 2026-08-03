package scenario

import (
	"github.com/justrunme/architecture-rehearsal/internal/graph"
	"github.com/justrunme/architecture-rehearsal/internal/loader"
)

const (
	RollbackAvailable   = "available"
	RollbackUnavailable = "unavailable"
	RollbackUnknown     = "unknown"

	// Outcome for a scenario evaluation (not the gate decision).
	OutcomeMatched    = "matched"
	OutcomeNotMatched = "not_matched"
	OutcomeUnknown    = "unknown"
)

// Requirement is a graph/fact prerequisite for a scenario.
type Requirement struct {
	ID      string `json:"id"`
	Message string `json:"message"`
}

// Result is a structured scenario evaluation.
type Result struct {
	Outcome  string    // matched | not_matched | unknown
	Findings []Finding
	Missing  []Requirement
}

// Finding is one predicted issue.
type Finding struct {
	ID         string   `json:"id"`
	Scenario   string   `json:"scenario"`
	Risk       string   `json:"risk"`
	Title      string   `json:"title"`
	Summary    string   `json:"summary"`
	Components []string `json:"components,omitempty"`
	Cascade    []string `json:"cascade,omitempty"`
	Controls   []string `json:"recommended_controls,omitempty"`
	SLOImpact  string   `json:"slo_impact,omitempty"`
	Evidence   []string `json:"evidence,omitempty"`
	Rollback   string   `json:"rollback"`
	Confidence string   `json:"confidence"`
}

// Context is input to all scenarios.
type Context struct {
	Baseline *graph.Snapshot
	Proposed *graph.Snapshot
	Change   *loader.ChangeEnvelope
	BaseIdx  *graph.Index
	PropIdx  *graph.Index
}

// Runner evaluates one deterministic pattern.
type Runner interface {
	Name() string
	// Applicable reports whether this change kind can trigger the scenario.
	Applicable(ctx Context) bool
	// MissingRequirements lists required coverage when applicable.
	MissingRequirements(ctx Context) []Requirement
	// Evaluate returns matched findings, not_matched, or unknown.
	Evaluate(ctx Context) Result
}

// DefaultRunners returns the v0.3 scenario set.
func DefaultRunners() []Runner {
	return []Runner{
		RWONodeLoss{},
		CNICapacity{},
		PromZeroMatch{},
		PDBDisruption{},
		ServiceRouting{},
		VolumeAZ{},
	}
}

// RunAll executes all runners and flattens findings; also returns unknown requirements.
func RunAll(ctx Context, runners []Runner) (findings []Finding, unknownReqs []Requirement) {
	for _, r := range runners {
		if !r.Applicable(ctx) {
			continue
		}
		if miss := r.MissingRequirements(ctx); len(miss) > 0 {
			unknownReqs = append(unknownReqs, miss...)
			// emit unknown finding so decision can go unknown with evidence
			findings = append(findings, Finding{
				ID:         "unknown:" + r.Name(),
				Scenario:   r.Name(),
				Risk:       "unknown",
				Title:      "Insufficient data for scenario " + r.Name(),
				Summary:    "Scenario is applicable but required graph/facts are missing; cannot safely approve.",
				Evidence:   reqMessages(miss),
				Rollback:   RollbackUnknown,
				Confidence: "low",
			})
			continue
		}
		res := r.Evaluate(ctx)
		switch res.Outcome {
		case OutcomeMatched:
			findings = append(findings, res.Findings...)
		case OutcomeUnknown:
			unknownReqs = append(unknownReqs, res.Missing...)
			findings = append(findings, res.Findings...)
		}
	}
	return findings, unknownReqs
}

func reqMessages(rr []Requirement) []string {
	var out []string
	for _, r := range rr {
		out = append(out, r.ID+": "+r.Message)
	}
	return out
}

func factString(m map[string]any, key, def string) string {
	return loader.FactString(m, key, def)
}
func factBool(m map[string]any, key string, def bool) bool {
	return loader.FactBool(m, key, def)
}
func factInt(m map[string]any, key string, def int) int {
	return loader.FactInt(m, key, def)
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

func display(idx *graph.Index, id string) string {
	if n := idx.ByID[id]; n != nil {
		if n.Namespace != "" {
			return n.Namespace + "/" + n.Name
		}
		return n.Name
	}
	return id
}

func hasKind(idx *graph.Index, k graph.Kind) bool {
	return idx != nil && len(idx.ByKind[k]) > 0
}

func changeKind(ch *loader.ChangeEnvelope) string {
	if ch == nil {
		return ""
	}
	return ch.EffectiveKind()
}
