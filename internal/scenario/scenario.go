// Package scenario contains deterministic failure-pattern detectors.
// Graph and rules decide; AI (later) only explains.
package scenario

import (
	"github.com/justrunme/architecture-rehearsal/internal/graph"
	"github.com/justrunme/architecture-rehearsal/internal/loader"
)

// Finding is one predicted issue.
type Finding struct {
	ID          string   `json:"id"`
	Scenario    string   `json:"scenario"`
	Risk        string   `json:"risk"` // none|low|medium|high|critical
	Title       string   `json:"title"`
	Summary     string   `json:"summary"`
	Components  []string `json:"components,omitempty"`
	Cascade     []string `json:"cascade,omitempty"`
	Controls    []string `json:"recommended_controls,omitempty"`
	SLOImpact   string   `json:"slo_impact,omitempty"`
	Evidence    []string `json:"evidence,omitempty"`
	RollbackOK  bool     `json:"rollback_available"`
	Confidence  string   `json:"confidence"` // high|medium|low
}

// Context is input to all scenarios.
type Context struct {
	Baseline *graph.Snapshot
	Proposed *graph.Snapshot
	Change   *loader.ChangeEnvelope
	BaseIdx  *graph.Index
	PropIdx  *graph.Index
}

// Runner is a named deterministic detector.
type Runner interface {
	Name() string
	Run(ctx Context) []Finding
}

// DefaultRunners returns the v0.1 golden trio.
func DefaultRunners() []Runner {
	return []Runner{
		RWONodeLoss{},
		CNICapacity{},
		PromZeroMatch{},
	}
}

// RunAll executes all runners.
func RunAll(ctx Context, runners []Runner) []Finding {
	var out []Finding
	for _, r := range runners {
		out = append(out, r.Run(ctx)...)
	}
	return out
}
