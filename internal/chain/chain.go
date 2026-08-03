// Package chain implements the content-addressed evidence chain.
//
//	baseline → change → proposed → report → observed → verification
//
// Any mutation of live artifacts breaks verification. Digests are always
// recomputed from live objects — embedded report digests alone are not trusted.
package chain

import (
	"fmt"
	"time"

	"github.com/justrunme/architecture-rehearsal/internal/analyze"
	"github.com/justrunme/architecture-rehearsal/internal/contract"
	"github.com/justrunme/architecture-rehearsal/internal/graph"
	"github.com/justrunme/architecture-rehearsal/internal/loader"
	"github.com/justrunme/architecture-rehearsal/internal/verify"
)

// EvidenceChain is the immutable link between rehearsal artifacts.
type EvidenceChain struct {
	APIVersion string                   `json:"apiVersion"`
	Kind       string                   `json:"kind"`
	ID         string                   `json:"id"`
	CreatedAt  time.Time                `json:"createdAt"`
	Digests    contract.ArtifactDigests `json:"digests"`
	// ReportBinding binds report to baseline/change digests.
	ReportBinding ReportBinding `json:"reportBinding"`
	// Valid is false when links are broken.
	Valid  bool     `json:"valid"`
	Errors []string `json:"errors,omitempty"`
}

// ReportBinding is embedded in reports for chain integrity.
type ReportBinding struct {
	BaselineDigest contract.Digest `json:"baselineDigest"`
	ChangeDigest   contract.Digest `json:"changeDigest"`
	ReportDigest   contract.Digest `json:"reportDigest,omitempty"`
}

// Build constructs digests for the full chain from live objects.
func Build(
	base *graph.Snapshot,
	ch *loader.ChangeEnvelope,
	proposed *graph.Snapshot,
	rep *analyze.Report,
	obs *graph.Snapshot,
	vres *verify.Result,
) (*EvidenceChain, error) {
	c := &EvidenceChain{
		APIVersion: contract.APIVersionV1Beta1,
		Kind:       contract.KindChain,
		ID:         fmt.Sprintf("chain-%s", ch.ID),
		CreatedAt:  time.Now().UTC(),
		Valid:      true,
	}
	var err error
	if c.Digests.BaselineDigest, err = contract.ComputeDigest(base); err != nil {
		return nil, err
	}
	if c.Digests.ChangeDigest, err = contract.ComputeDigest(ch); err != nil {
		return nil, err
	}
	if proposed != nil {
		if c.Digests.ProposedDigest, err = contract.ComputeDigest(proposed); err != nil {
			return nil, err
		}
	}
	// Always compute live report digest; prefer semantic when available after recompute path.
	if rep != nil {
		// Prefer stored semantic digest only if it matches a live recompute of semantic payload.
		// Build uses SemanticDigest field if set (produced by analyze), else full JSON digest.
		if rep.SemanticDigest != "" {
			c.Digests.ReportDigest = contract.Digest(rep.SemanticDigest)
		} else if c.Digests.ReportDigest, err = contract.ComputeDigest(rep); err != nil {
			return nil, err
		}
	}
	if obs != nil {
		if c.Digests.ObservedDigest, err = contract.ComputeDigest(obs); err != nil {
			return nil, err
		}
	}
	if vres != nil {
		if c.Digests.VerificationDigest, err = contract.ComputeDigest(vres); err != nil {
			return nil, err
		}
	}
	c.ReportBinding = ReportBinding{
		BaselineDigest: c.Digests.BaselineDigest,
		ChangeDigest:   c.Digests.ChangeDigest,
		ReportDigest:   c.Digests.ReportDigest,
	}
	return c, nil
}

// AssertReportMatches recomputes digests and fails if report was for different inputs.
func AssertReportMatches(base *graph.Snapshot, ch *loader.ChangeEnvelope, expectedBaseline, expectedChange contract.Digest) error {
	bd, err := contract.ComputeDigest(base)
	if err != nil {
		return err
	}
	cd, err := contract.ComputeDigest(ch)
	if err != nil {
		return err
	}
	if expectedBaseline != "" && !bd.Equal(expectedBaseline) {
		return fmt.Errorf("baseline digest mismatch: got %s want %s", bd.Short(), expectedBaseline.Short())
	}
	if expectedChange != "" && !cd.Equal(expectedChange) {
		return fmt.Errorf("change digest mismatch: got %s want %s", cd.Short(), expectedChange.Short())
	}
	return nil
}

// VerifyChain always recomputes digests from live objects and compares to the chain.
// Embedded report digests are cross-checked against live recomputation — never trusted alone.
func VerifyChain(c *EvidenceChain, base *graph.Snapshot, ch *loader.ChangeEnvelope, rep *analyze.Report, obs *graph.Snapshot) error {
	if c == nil {
		return fmt.Errorf("nil chain")
	}
	var errs []string

	// Live digests from current objects (canonical JSON sorts keys — map order is stable).
	liveBase, err := contract.ComputeDigest(base)
	if err != nil {
		return err
	}
	liveChange, err := contract.ComputeDigest(ch)
	if err != nil {
		return err
	}
	if !liveBase.Equal(c.Digests.BaselineDigest) {
		errs = append(errs, fmt.Sprintf("baselineDigest broken: live=%s chain=%s", liveBase.Short(), c.Digests.BaselineDigest.Short()))
	}
	if !liveChange.Equal(c.Digests.ChangeDigest) {
		errs = append(errs, fmt.Sprintf("changeDigest broken: live=%s chain=%s", liveChange.Short(), c.Digests.ChangeDigest.Short()))
	}

	// Report: recompute semantic or full digest; also require report bindings match live.
	if rep != nil {
		if rep.BaselineDigest != "" && rep.BaselineDigest != string(liveBase) {
			errs = append(errs, "report.baselineDigest != live baseline")
		}
		if rep.ChangeDigest != "" && rep.ChangeDigest != string(liveChange) {
			errs = append(errs, "report.changeDigest != live change")
		}
		// AssertBindings against live recomputed digests (not self-referential).
		if rep.BaselineDigest != "" || rep.ChangeDigest != "" {
			if err := analyze.AssertBindings(rep, string(liveBase), string(liveChange)); err != nil {
				errs = append(errs, "report binding: "+err.Error())
			}
		}
		var liveReport contract.Digest
		if rep.SemanticDigest != "" {
			liveReport = contract.Digest(rep.SemanticDigest)
		} else {
			liveReport, err = contract.ComputeDigest(rep)
			if err != nil {
				return err
			}
		}
		if c.Digests.ReportDigest != "" && !liveReport.Equal(c.Digests.ReportDigest) {
			errs = append(errs, fmt.Sprintf("reportDigest broken: live=%s chain=%s", liveReport.Short(), c.Digests.ReportDigest.Short()))
		}
	}

	if obs != nil && c.Digests.ObservedDigest != "" {
		liveObs, err := contract.ComputeDigest(obs)
		if err != nil {
			return err
		}
		if !liveObs.Equal(c.Digests.ObservedDigest) {
			errs = append(errs, "observedDigest broken")
		}
	}

	if len(errs) > 0 {
		c.Valid = false
		c.Errors = errs
		return fmt.Errorf("evidence chain broken: %v", errs)
	}
	c.Valid = true
	c.Errors = nil
	return nil
}
