// Package calibrate tracks scenario prediction quality (v0.13).
package calibrate

import (
	"sync"
	"time"

	"github.com/justrunme/architecture-rehearsal/internal/contract"
)

// Class is the outcome class.
type Class string

const (
	TruePositive  Class = "true_positive"
	FalsePositive Class = "false_positive"
	TrueNegative  Class = "true_negative"
	FalseNegative Class = "false_negative"
	Inconclusive  Class = "inconclusive"
)

// Outcome is one prediction vs observation.
type Outcome struct {
	Scenario  string
	Predicted bool // predicted failure
	Observed  bool // failure observed
	Verified  bool // verification completed
}

// Classify maps predicted/observed to class.
func Classify(o Outcome) Class {
	if !o.Verified {
		return Inconclusive
	}
	switch {
	case o.Predicted && o.Observed:
		return TruePositive
	case o.Predicted && !o.Observed:
		return FalsePositive
	case !o.Predicted && !o.Observed:
		return TrueNegative
	default:
		return FalseNegative
	}
}

// ConfidenceFactors are additive deterministic confidence inputs (0..1 total cap).
type ConfidenceFactors struct {
	OwnerReferencePresent   bool
	PVCToPVEdge             bool
	FailedAttachVolumeEvent bool
	LostNodeConfirmed       bool
	ObservedWithinWindow    bool
}

// Confidence returns 0..1 score.
func Confidence(f ConfidenceFactors) float64 {
	var s float64
	if f.OwnerReferencePresent {
		s += 0.15
	}
	if f.PVCToPVEdge {
		s += 0.20
	}
	if f.FailedAttachVolumeEvent {
		s += 0.30
	}
	if f.LostNodeConfirmed {
		s += 0.20
	}
	if f.ObservedWithinWindow {
		s += 0.15
	}
	if s > 1 {
		s = 1
	}
	// avoid float noise
	return float64(int(s*1000+0.5)) / 1000
}

// Stats is per-scenario quality.
type Stats struct {
	Scenario            string  `json:"scenario"`
	EvaluatedRuns       int     `json:"evaluatedRuns"`
	VerifiedPredictions int     `json:"verifiedPredictions"`
	TruePositives       int     `json:"truePositives"`
	FalsePositives      int     `json:"falsePositives"`
	TrueNegatives       int     `json:"trueNegatives"`
	FalseNegatives      int     `json:"falseNegatives"`
	Inconclusive        int     `json:"inconclusive"`
	Precision           float64 `json:"precision"`
	Recall              float64 `json:"recall"`
	FalsePositiveRate   float64 `json:"falsePositiveRate"`
}

// Report is the calibration document.
type Report struct {
	APIVersion string    `json:"apiVersion"`
	Kind       string    `json:"kind"`
	Generated  time.Time `json:"generatedAt"`
	Scenarios  []Stats   `json:"scenarios"`
}

// Store accumulates outcomes.
type Store struct {
	mu   sync.Mutex
	data map[string]*Stats
}

// NewStore creates an empty calibration store.
func NewStore() *Store {
	return &Store{data: map[string]*Stats{}}
}

// Record adds one outcome.
func (s *Store) Record(o Outcome) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.data[o.Scenario]
	if st == nil {
		st = &Stats{Scenario: o.Scenario}
		s.data[o.Scenario] = st
	}
	st.EvaluatedRuns++
	c := Classify(o)
	switch c {
	case TruePositive:
		st.TruePositives++
		st.VerifiedPredictions++
	case FalsePositive:
		st.FalsePositives++
		st.VerifiedPredictions++
	case TrueNegative:
		st.TrueNegatives++
	case FalseNegative:
		st.FalseNegatives++
	case Inconclusive:
		st.Inconclusive++
	}
	st.recompute()
}

func (st *Stats) recompute() {
	// precision = TP / (TP+FP)
	den := st.TruePositives + st.FalsePositives
	if den > 0 {
		st.Precision = float64(st.TruePositives) / float64(den)
	}
	// recall = TP / (TP+FN)
	denR := st.TruePositives + st.FalseNegatives
	if denR > 0 {
		st.Recall = float64(st.TruePositives) / float64(denR)
	}
	// FPR = FP / (FP+TN)
	denF := st.FalsePositives + st.TrueNegatives
	if denF > 0 {
		st.FalsePositiveRate = float64(st.FalsePositives) / float64(denF)
	}
}

// Report builds the calibration report.
func (s *Store) Report() Report {
	s.mu.Lock()
	defer s.mu.Unlock()
	rep := Report{
		APIVersion: contract.APIVersionV1Beta1,
		Kind:       contract.KindCalibration,
		Generated:  time.Now().UTC(),
	}
	for _, st := range s.data {
		cp := *st
		rep.Scenarios = append(rep.Scenarios, cp)
	}
	return rep
}
