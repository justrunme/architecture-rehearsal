// Package impact implements Argo CD change-impact detectors.
// Each detector returns PASS, WARN, BLOCK, or UNKNOWN with explanation and confidence.
// UNKNOWN is required when data is missing — detectors never silently PASS.
package impact

import (
	"github.com/justrunme/architecture-rehearsal/internal/graph"
	"github.com/justrunme/architecture-rehearsal/internal/loader"
	"github.com/justrunme/architecture-rehearsal/internal/scenario"
)

// Verdict status values (gate-facing, not scenario OutcomeMatched).
const (
	Pass    = "PASS"
	Warn    = "WARN"
	Block   = "BLOCK"
	Unknown = "UNKNOWN"
)

// Verdict is one detector result.
type Verdict struct {
	Detector   string   `json:"detector"`
	Status     string   `json:"status"` // PASS|WARN|BLOCK|UNKNOWN
	Explanation string  `json:"explanation"`
	Confidence string   `json:"confidence"` // high|medium|low
	Evidence   []string `json:"evidence,omitempty"`
	Affected   []string `json:"affected,omitempty"`
	Missing    []string `json:"missing,omitempty"`
}

// Report aggregates detector verdicts into a gate-facing impact report.
type Report struct {
	AppName    string    `json:"appName,omitempty"`
	ChangeID   string    `json:"changeId,omitempty"`
	Revision   string    `json:"revision,omitempty"`
	Verdict    string    `json:"gateVerdict"` // PASS|WARN|BLOCK|UNKNOWN
	Detectors  []Verdict `json:"detectors"`
	Affected   []string  `json:"affectedWorkloads,omitempty"`
	Predicted  []string  `json:"predictedFailures,omitempty"`
	Missing    []string  `json:"missingData,omitempty"`
	Confidence string    `json:"confidence"`
	Summary    string    `json:"summary"`
	// AnalyzeDecision mirrors analyze.Report.Decision (approve|warn|block|unknown).
	AnalyzeDecision string `json:"analyzeDecision,omitempty"`
	AnalyzeRisk     string `json:"analyzeRisk,omitempty"`
}

// Context is detector input (mirrors scenario.Context).
type Context struct {
	Baseline *graph.Snapshot
	Proposed *graph.Snapshot
	Change   *loader.ChangeEnvelope
	BaseIdx  *graph.Index
	PropIdx  *graph.Index
	// PDBsCollected is true when the baseline inventory included PDB collection
	// (even if the set is empty). When false, absence of PDBs is UNKNOWN not PASS.
	PDBsCollected bool
}

// Detector evaluates one impact concern.
type Detector interface {
	Name() string
	Evaluate(ctx Context) Verdict
}

// FromScenario builds impact.Context from scenario.Context.
func FromScenario(ctx scenario.Context) Context {
	return Context{
		Baseline:      ctx.Baseline,
		Proposed:      ctx.Proposed,
		Change:        ctx.Change,
		BaseIdx:       ctx.BaseIdx,
		PropIdx:       ctx.PropIdx,
		PDBsCollected: pdbsInventoryKnown(ctx.Baseline),
	}
}

// DefaultDetectors returns the Argo impact detector set.
func DefaultDetectors() []Detector {
	return []Detector{
		SchedulingCapacity{},
		PVCZoneConflict{},
		NetworkPolicyCut{},
		PDBRollout{},
		BlastRadius{},
	}
}

// Aggregate picks the strongest detector status as the gate verdict.
// Empty detector list or any empty/unknown status → UNKNOWN (fail-closed).
func Aggregate(vs []Verdict) string {
	if len(vs) == 0 {
		return Unknown
	}
	order := map[string]int{Pass: 0, Warn: 1, Unknown: 2, Block: 3}
	best := Pass
	for _, v := range vs {
		st := v.Status
		if st == "" {
			st = Unknown
		}
		if _, ok := order[st]; !ok {
			st = Unknown
		}
		if order[st] > order[best] {
			best = st
		}
	}
	return best
}

// ToAnalyzeDecision maps impact gate verdict to analyze decision vocabulary.
func ToAnalyzeDecision(v string) string {
	switch v {
	case Pass:
		return "approve"
	case Warn:
		return "warn"
	case Block:
		return "block"
	default:
		return "unknown"
	}
}

// ExitCode maps gate verdict to CLI exit codes (ADR-0001).
func ExitCode(v string) int {
	switch v {
	case Pass:
		return 0
	case Warn:
		return 1
	case Block:
		return 3
	case Unknown:
		return 4
	default:
		return 5
	}
}

func isArgoChange(ch *loader.ChangeEnvelope) bool {
	if ch == nil {
		return false
	}
	k := ch.EffectiveKind()
	if k == "argo-sync" || k == "k8s-manifest" || k == "helm-upgrade" {
		return true
	}
	if ch.Facts == nil {
		return false
	}
	s, _ := ch.Facts["scenario"].(string)
	return s == "argo-impact" || s == "cni-ip-capacity"
}
