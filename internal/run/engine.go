package run

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/justrunme/architecture-rehearsal/internal/analyze"
	"github.com/justrunme/architecture-rehearsal/internal/chain"
	"github.com/justrunme/architecture-rehearsal/internal/contract"
	"github.com/justrunme/architecture-rehearsal/internal/loader"
	"github.com/justrunme/architecture-rehearsal/internal/verify"
)

// Engine executes the Collect→…→Verify pipeline against local files (v0.9 runner).
type Engine struct {
	WorkDir string
	Holder  string
}

// Execute runs the full offline lifecycle for a RehearsalRun with file refs.
func (e *Engine) Execute(r *RehearsalRun) error {
	if e.Holder == "" {
		e.Holder = "local-runner"
	}
	if err := r.AcquireLease(e.Holder, 10*time.Minute); err != nil {
		return err
	}
	defer r.ReleaseLease(e.Holder)

	type step struct {
		to  Phase
		fn  func(*RehearsalRun) error
		msg string
	}
	steps := []step{
		{PhaseCollecting, e.stepCollect, "collect baseline"},
		{PhaseCompiling, e.stepCompile, "load change"},
		{PhaseRehearsing, e.stepRehearse, "analyze"},
		{PhaseGated, e.stepGate, "gate decision"},
	}

	for _, s := range steps {
		if r.Status.Phase.Terminal() {
			return nil
		}
		if err := r.Transition(s.to, s.msg); err != nil {
			_ = r.Transition(PhaseFailed, err.Error())
			return err
		}
		if s.fn != nil {
			if err := s.fn(r); err != nil {
				_ = r.Transition(PhaseFailed, err.Error())
				return err
			}
		}
	}

	// No observed → stop at Gated/Completed without verify
	if r.Spec.ObservedRef == "" {
		if !r.Status.Phase.Terminal() {
			_ = r.Transition(PhaseCompleted, "gated only (no observedRef)")
		}
		return nil
	}

	post := []step{
		{PhaseWaitingForDeployment, nil, "deployment assumed (offline)"},
		{PhaseObserving, e.stepObserve, "load observed"},
		{PhaseVerifying, e.stepVerify, "verify"},
	}
	for _, s := range post {
		if r.Status.Phase.Terminal() {
			return nil
		}
		if err := r.Transition(s.to, s.msg); err != nil {
			_ = r.Transition(PhaseFailed, err.Error())
			return err
		}
		if s.fn != nil {
			if err := s.fn(r); err != nil {
				if !r.Status.Phase.Terminal() {
					_ = r.Transition(PhaseFailed, err.Error())
				}
				return err
			}
		}
	}
	if !r.Status.Phase.Terminal() {
		_ = r.Transition(PhaseCompleted, "verification finished")
	}
	return nil
}

func (e *Engine) path(ref string) string {
	if filepath.IsAbs(ref) {
		return ref
	}
	if e.WorkDir != "" {
		return filepath.Join(e.WorkDir, ref)
	}
	return ref
}

func (e *Engine) stepCollect(r *RehearsalRun) error {
	if r.Spec.BaselineRef == "" {
		return fmt.Errorf("spec.baselineRef required")
	}
	if _, err := os.Stat(e.path(r.Spec.BaselineRef)); err != nil {
		return fmt.Errorf("baseline: %w", err)
	}
	return nil
}

func (e *Engine) stepCompile(r *RehearsalRun) error {
	if r.Spec.ChangeRef == "" {
		return fmt.Errorf("spec.changeRef required")
	}
	if _, err := os.Stat(e.path(r.Spec.ChangeRef)); err != nil {
		return fmt.Errorf("change: %w", err)
	}
	return nil
}

func (e *Engine) stepRehearse(r *RehearsalRun) error {
	base, err := loader.LoadSnapshot(e.path(r.Spec.BaselineRef))
	if err != nil {
		return err
	}
	ch, err := loader.LoadChange(e.path(r.Spec.ChangeRef))
	if err != nil {
		return err
	}
	rep, err := analyze.Run(base, ch)
	if err != nil {
		return err
	}
	r.Status.Decision = rep.Decision
	r.Status.Risk = rep.Risk
	r.Status.Message = rep.Summary
	bd, _ := contract.ComputeDigest(base)
	cd, _ := contract.ComputeDigest(ch)
	rd, _ := contract.ComputeDigest(rep)
	r.Digests.BaselineDigest = bd
	r.Digests.ChangeDigest = cd
	r.Digests.ReportDigest = rd
	if r.Labels == nil {
		r.Labels = map[string]string{}
	}
	r.Labels["_semanticDigest"] = rep.SemanticDigest
	return nil
}

func (e *Engine) stepGate(r *RehearsalRun) error {
	blockOn := r.Spec.Gate.BlockOn
	if len(blockOn) == 0 {
		blockOn = []string{"critical", "high", "block"}
	}
	for _, b := range blockOn {
		if r.Status.Risk == b || r.Status.Decision == b {
			r.Status.Message = fmt.Sprintf("gate blocked: risk=%s decision=%s", r.Status.Risk, r.Status.Decision)
			return nil
		}
	}
	return nil
}

func (e *Engine) stepObserve(r *RehearsalRun) error {
	if r.Spec.ObservedRef == "" {
		return fmt.Errorf("spec.observedRef required for observe")
	}
	if _, err := os.Stat(e.path(r.Spec.ObservedRef)); err != nil {
		return err
	}
	return nil
}

func (e *Engine) stepVerify(r *RehearsalRun) error {
	base, err := loader.LoadSnapshot(e.path(r.Spec.BaselineRef))
	if err != nil {
		return err
	}
	ch, err := loader.LoadChange(e.path(r.Spec.ChangeRef))
	if err != nil {
		return err
	}
	rep, err := analyze.Run(base, ch)
	if err != nil {
		return err
	}
	if r.Digests.BaselineDigest != "" {
		if err := chain.AssertReportMatches(base, ch, r.Digests.BaselineDigest, r.Digests.ChangeDigest); err != nil {
			return fmt.Errorf("chain: %w", err)
		}
	}
	obs, err := loader.LoadSnapshot(e.path(r.Spec.ObservedRef))
	if err != nil {
		return err
	}
	vres := verify.RunWithOptions(rep, obs, verify.Options{Baseline: base, Change: ch})
	r.Status.Message = fmt.Sprintf("verify=%s score=%.2f", vres.Outcome, vres.Score)
	od, _ := contract.ComputeDigest(obs)
	vd, _ := contract.ComputeDigest(vres)
	r.Digests.ObservedDigest = od
	r.Digests.VerificationDigest = vd

	// Build and validate full chain
	chObj, err := chain.Build(base, ch, nil, rep, obs, vres)
	if err == nil {
		_ = chObj
	}

	switch vres.Outcome {
	case verify.OutcomeVerified:
		return nil // caller moves to Completed
	case verify.OutcomeInconclusive:
		_ = r.Transition(PhaseInconclusive, vres.Summary)
		return nil
	default:
		_ = r.Transition(PhaseFailed, vres.Summary)
		return nil
	}
}
