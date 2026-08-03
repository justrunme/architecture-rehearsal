package analyze

import (
	"time"

	"github.com/justrunme/architecture-rehearsal/internal/scenario"
)

// Risk levels (ordered).
const (
	RiskNone     = "none"
	RiskLow      = "low"
	RiskMedium   = "medium"
	RiskHigh     = "high"
	RiskCritical = "critical"
	RiskUnknown  = "unknown"
)

// Decision gate recommendations.
const (
	DecisionApprove = "approve"
	DecisionWarn    = "warn"
	DecisionBlock   = "block"
	DecisionUnknown = "unknown"
)

// RollbackStatus is ternary — never claim yes without evidence.
const (
	RollbackAvailable   = "available"
	RollbackUnavailable = "unavailable"
	RollbackUnknown     = "unknown"
)

// Coverage summarizes domain completeness (not an AI score).
type Coverage struct {
	Overall        float64            `json:"overall"`
	Domains        map[string]float64 `json:"domains,omitempty"`
	RequiredMissing []string          `json:"requiredMissing,omitempty"`
	Gaps           []string           `json:"gaps,omitempty"`
}

// Report is the full analyze output.
type Report struct {
	APIVersion string    `json:"apiVersion,omitempty"`
	Kind       string    `json:"kind,omitempty"`
	Version    string    `json:"version"`
	Generated  time.Time `json:"generatedAt"`
	// SemanticDigest excludes runtime timestamps for deterministic compare.
	SemanticDigest string `json:"semanticDigest,omitempty"`

	ChangeID    string `json:"changeId"`
	ChangeTitle string `json:"changeTitle"`
	ChangeKind  string `json:"changeKind"`
	BaselineID  string `json:"baselineId"`

	Risk     string `json:"risk"`
	Decision string `json:"decision"`
	Summary  string `json:"summary"`

	AffectedComponents int      `json:"affected_components"`
	CriticalPaths      int      `json:"critical_paths"`
	SLOViolations      int      `json:"slo_violations"`
	Rollback           string   `json:"rollback"` // available|unavailable|unknown
	PredictedFailures  []string `json:"predicted_failures"`

	Findings []scenario.Finding `json:"findings"`
	Coverage Coverage           `json:"coverage"`
	Cascades [][]string         `json:"cascades,omitempty"`

	// Deprecated: prefer Coverage.Gaps
	CoverageGaps []string `json:"coverage_gaps,omitempty"`
}

// MaxRisk returns the higher of two risk levels.
func MaxRisk(a, b string) string {
	order := map[string]int{
		RiskNone: 0, RiskLow: 1, RiskMedium: 2, RiskHigh: 3, RiskCritical: 4, RiskUnknown: 5,
	}
	// unknown elevates decision but not always above critical for display —
	// keep critical as highest display risk; unknown handled in DecisionFrom.
	if a == RiskUnknown {
		return b
	}
	if b == RiskUnknown {
		return a
	}
	if order[b] > order[a] {
		return b
	}
	return a
}

// DecisionFromFindings maps findings + coverage to a gate decision.
// Missing required data never becomes approve.
// Confident high/critical findings still block even if other scenarios lack data.
func DecisionFromFindings(risk string, findings []scenario.Finding, cov Coverage, insufficient bool) string {
	hasBlock := false
	hasWarn := false
	for _, f := range findings {
		if f.Risk == RiskUnknown {
			continue
		}
		if f.Risk == RiskCritical || f.Risk == RiskHigh {
			hasBlock = true
		}
		if f.Risk == RiskMedium {
			hasWarn = true
		}
	}
	if hasBlock || risk == RiskCritical || risk == RiskHigh {
		return DecisionBlock
	}
	if insufficient || len(cov.RequiredMissing) > 0 {
		return DecisionUnknown
	}
	if hasWarn || risk == RiskMedium {
		return DecisionWarn
	}
	if risk == RiskUnknown {
		return DecisionUnknown
	}
	return DecisionApprove
}
