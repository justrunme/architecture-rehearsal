package run

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/justrunme/architecture-rehearsal/internal/analyze"
	"github.com/justrunme/architecture-rehearsal/internal/calibrate"
	"github.com/justrunme/architecture-rehearsal/internal/chain"
	"github.com/justrunme/architecture-rehearsal/internal/contract"
	"github.com/justrunme/architecture-rehearsal/internal/evidence"
	"github.com/justrunme/architecture-rehearsal/internal/loader"
	"github.com/justrunme/architecture-rehearsal/internal/policy"
	"github.com/justrunme/architecture-rehearsal/internal/verify"
)

// Engine executes the Collect→…→Verify pipeline against local files.
type Engine struct {
	WorkDir   string
	Holder    string
	Calibrate *calibrate.Store // optional
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

	if r.Spec.ObservedRef == "" {
		if !r.Status.Phase.Terminal() {
			// Gated-only: do NOT mark as verified calibration success
			_ = r.Transition(PhaseCompleted, "gated only (no observedRef); not calibrated as verified")
		}
		return nil
	}

	post := []step{
		{PhaseWaitingForDeployment, nil, "deployment assumed (offline)"},
		{PhaseObserving, e.stepObserve, "load observed"},
		{PhaseVerifying, e.stepVerify, "verify + persist chain"},
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

func (e *Engine) outDir(r *RehearsalRun) string {
	if r.Spec.OutDir != "" {
		return e.path(r.Spec.OutDir)
	}
	if e.WorkDir != "" {
		return filepath.Join(e.WorkDir, "out", r.ID)
	}
	return filepath.Join("out", r.ID)
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
	r.Status.PredictedFailures = append([]string{}, rep.PredictedFailures...)
	r.Digests.BaselineDigest = contract.Digest(rep.BaselineDigest)
	r.Digests.ChangeDigest = contract.Digest(rep.ChangeDigest)
	r.Digests.ProposedDigest = contract.Digest(rep.ProposedDigest)
	r.Digests.ReportDigest = contract.Digest(rep.SemanticDigest)
	if r.Labels == nil {
		r.Labels = map[string]string{}
	}
	r.Labels["_semanticDigest"] = rep.SemanticDigest

	// Persist report for chain
	dir := e.outDir(r)
	_ = os.MkdirAll(dir, 0o755)
	rb, _ := json.MarshalIndent(rep, "", "  ")
	_ = os.WriteFile(filepath.Join(dir, "report.json"), rb, 0o644)
	r.Labels["_reportPath"] = filepath.Join(dir, "report.json")
	return nil
}

func (e *Engine) stepGate(r *RehearsalRun) error {
	// Prefer organization policy when path provided
	doc, err := policy.Load(e.path(r.Spec.PolicyPath))
	if err != nil {
		return fmt.Errorf("policy: %w", err)
	}
	// Reload coverage gaps count from report if available
	missing := 0
	if p := r.Labels["_reportPath"]; p != "" {
		if raw, err := os.ReadFile(p); err == nil {
			var rep analyze.Report
			if json.Unmarshal(raw, &rep) == nil {
				missing = len(rep.Coverage.RequiredMissing)
			}
		}
	}
	pres := policy.Evaluate(doc, policy.Input{
		Risk: r.Status.Risk, Decision: r.Status.Decision,
		Rollback: "unknown", RequiredMissing: missing,
	})
	// Merge: policy can only escalate (block > warn > approve)
	if pres.Decision == "block" || (pres.Decision == "warn" && r.Status.Decision == "approve") {
		r.Status.Decision = pres.Decision
		r.Status.Message = fmt.Sprintf("policy:%s matched=%v · %s", pres.Decision, pres.Matched, r.Status.Message)
	}

	// Spec.Gate.BlockOn still honored as hard block list on risk/decision
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
	// Prefer persisted report (with digests) over re-analyze for binding stability
	var rep *analyze.Report
	if p := r.Labels["_reportPath"]; p != "" {
		raw, err := os.ReadFile(p)
		if err == nil {
			var loaded analyze.Report
			if json.Unmarshal(raw, &loaded) == nil && loaded.BaselineDigest != "" {
				rep = &loaded
			}
		}
	}
	if rep == nil {
		rep, err = analyze.Run(base, ch)
		if err != nil {
			return err
		}
	}
	obs, err := loader.LoadSnapshot(e.path(r.Spec.ObservedRef))
	if err != nil {
		return err
	}
	vres := verify.RunWithOptions(rep, obs, verify.Options{Baseline: base, Change: ch})
	r.Status.VerifyOutcome = vres.Outcome
	r.Status.Message = fmt.Sprintf("verify=%s score=%.2f", vres.Outcome, vres.Score)
	if vres.ObservedDigest != "" {
		r.Digests.ObservedDigest = contract.Digest(vres.ObservedDigest)
	}
	if vres.ReportDigest != "" {
		r.Digests.ReportDigest = contract.Digest(vres.ReportDigest)
	}
	vd, _ := contract.ComputeDigest(vres)
	r.Digests.VerificationDigest = vd

	// Persist full chain + verification + optional DSSE
	dir := e.outDir(r)
	_ = os.MkdirAll(dir, 0o755)
	chObj, err := chain.Build(base, ch, nil, rep, obs, vres)
	if err != nil {
		return err
	}
	// Align digests with report bindings
	chObj.Digests.BaselineDigest = contract.Digest(rep.BaselineDigest)
	chObj.Digests.ChangeDigest = contract.Digest(rep.ChangeDigest)
	chObj.Digests.ProposedDigest = contract.Digest(rep.ProposedDigest)
	chObj.ReportBinding = chain.ReportBinding{
		BaselineDigest: chObj.Digests.BaselineDigest,
		ChangeDigest:   chObj.Digests.ChangeDigest,
		ReportDigest:   contract.Digest(rep.SemanticDigest),
	}
	if err := chain.VerifyChain(chObj, base, ch, rep, obs); err != nil {
		// still write broken chain for forensics
		r.Status.Message += " · chain:" + err.Error()
	}
	cb, _ := json.MarshalIndent(chObj, "", "  ")
	chainPath := filepath.Join(dir, "evidence-chain.json")
	_ = os.WriteFile(chainPath, cb, 0o644)
	r.Status.ChainPath = chainPath

	vb, _ := json.MarshalIndent(vres, "", "  ")
	_ = os.WriteFile(filepath.Join(dir, "verification.json"), vb, 0o644)

	if sec := evidence.SecretFromEnv(); len(sec) > 0 {
		stmt := evidence.EvidenceStatement{
			ChangeID:             rep.ChangeID,
			Decision:             rep.Decision,
			Risk:                 rep.Risk,
			ChainDigests:         chObj.Digests,
			ReportSemanticDigest: rep.SemanticDigest,
			KeyID:                "hmac-env",
		}
		if env, err := evidence.SignEvidenceStatement(stmt, sec); err == nil {
			eb, _ := json.MarshalIndent(env, "", "  ")
			_ = os.WriteFile(filepath.Join(dir, "evidence-dsse.json"), eb, 0o644)
		}
	}

	// Per-scenario calibration (only when verification completed)
	if e.Calibrate != nil {
		verified := vres.Outcome != verify.OutcomeInconclusive
		for _, sc := range rep.PredictedFailures {
			// Predicted failure for this scenario
			// Observed: scenario check passed
			obsOK := false
			for _, c := range vres.Checks {
				if c.Name == "scenario:"+sc && c.Passed && !c.Unknown {
					obsOK = true
				}
			}
			e.Calibrate.Record(calibrate.Outcome{
				Scenario:  sc,
				Predicted: true,
				Observed:  obsOK,
				Verified:  verified,
			})
		}
	}

	switch vres.Outcome {
	case verify.OutcomeVerified:
		return nil
	case verify.OutcomeInconclusive:
		_ = r.Transition(PhaseInconclusive, vres.Summary)
		return nil
	default:
		_ = r.Transition(PhaseFailed, vres.Summary)
		return nil
	}
}
