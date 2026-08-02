// Package analyze produces change impact reports from graph + scenarios.
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
)

// Decision is the gate recommendation.
const (
	DecisionApprove = "approve"
	DecisionWarn    = "warn"
	DecisionBlock   = "block"
)

// Report is the full analyze output (JSON + HTML source of truth).
type Report struct {
	Version   string    `json:"version"`
	Generated time.Time `json:"generatedAt"`

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
	RollbackAvailable  bool     `json:"rollback_available"`
	PredictedFailures  []string `json:"predicted_failures"`

	Findings []scenario.Finding `json:"findings"`
	// CoverageGaps are honest unknowns (partial graph).
	CoverageGaps []string `json:"coverage_gaps,omitempty"`

	// Cascades are human-readable failure chains.
	Cascades [][]string `json:"cascades,omitempty"`
}

// MaxRisk returns the higher of two risk levels.
func MaxRisk(a, b string) string {
	order := map[string]int{
		RiskNone: 0, RiskLow: 1, RiskMedium: 2, RiskHigh: 3, RiskCritical: 4,
	}
	if order[b] > order[a] {
		return b
	}
	return a
}

// DecisionFromRisk maps risk to approve/warn/block.
func DecisionFromRisk(risk string) string {
	switch risk {
	case RiskCritical, RiskHigh:
		return DecisionBlock
	case RiskMedium:
		return DecisionWarn
	default:
		return DecisionApprove
	}
}
