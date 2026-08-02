package scenario

import (
	"github.com/justrunme/architecture-rehearsal/internal/graph"
	"github.com/justrunme/architecture-rehearsal/internal/loader"
)

const (
	RollbackAvailable   = "available"
	RollbackUnavailable = "unavailable"
	RollbackUnknown     = "unknown"
)

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
	Rollback   string   `json:"rollback"` // available|unavailable|unknown
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

// Runner is a named deterministic detector.
type Runner interface {
	Name() string
	Run(ctx Context) []Finding
}

// DefaultRunners returns production scenario set (v1.0).
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

// RunAll executes all runners.
func RunAll(ctx Context, runners []Runner) []Finding {
	var out []Finding
	for _, r := range runners {
		out = append(out, r.Run(ctx)...)
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
