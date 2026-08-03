// Package chain implements the content-addressed evidence chain (v0.8).
//
//	baseline → change → proposed → report → observed → verification
//
// Any mutation breaks the chain; verify refuses mismatched digests.
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
	APIVersion string                  `json:"apiVersion"`
	Kind       string                  `json:"kind"`
	ID         string                  `json:"id"`
	CreatedAt  time.Time               `json:"createdAt"`
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

// Build constructs digests for the full chain.
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
	if c.Digests.ReportDigest, err = contract.ComputeDigest(rep); err != nil {
		return nil, err
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

// VerifyChain recomputes digests from live objects and compares to chain.
func VerifyChain(c *EvidenceChain, base *graph.Snapshot, ch *loader.ChangeEnvelope, rep *analyze.Report, obs *graph.Snapshot) error {
	if c == nil {
		return fmt.Errorf("nil chain")
	}
	live, err := Build(base, ch, nil, rep, obs, nil)
	if err != nil {
		return err
	}
	var errs []string
	if !live.Digests.BaselineDigest.Equal(c.Digests.BaselineDigest) {
		errs = append(errs, "baselineDigest broken")
	}
	if !live.Digests.ChangeDigest.Equal(c.Digests.ChangeDigest) {
		errs = append(errs, "changeDigest broken")
	}
	if !live.Digests.ReportDigest.Equal(c.Digests.ReportDigest) {
		errs = append(errs, "reportDigest broken")
	}
	if obs != nil && c.Digests.ObservedDigest != "" && !live.Digests.ObservedDigest.Equal(c.Digests.ObservedDigest) {
		errs = append(errs, "observedDigest broken")
	}
	if len(errs) > 0 {
		c.Valid = false
		c.Errors = errs
		return fmt.Errorf("evidence chain broken: %v", errs)
	}
	c.Valid = true
	return nil
}
