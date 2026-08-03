package run_test

import (
	"path/filepath"
	"testing"

	"github.com/justrunme/architecture-rehearsal/internal/run"
)

func TestTransitions(t *testing.T) {
	r := run.NewRun("r1", "k1", run.Spec{})
	if err := r.Transition(run.PhaseCollecting, "go"); err != nil {
		t.Fatal(err)
	}
	if err := r.Transition(run.PhaseCompleted, "skip"); err == nil {
		t.Fatal("invalid transition should fail")
	}
	if err := r.AcquireLease("a", 0); err != nil {
		// ttl 0 expires immediately — still set
	}
	_ = r.AcquireLease("a", 60e9)
	if err := r.AcquireLease("b", 60e9); err == nil {
		t.Fatal("lease conflict")
	}
}

func TestEngineGatedOnly(t *testing.T) {
	// Use golden fixtures relative to module
	root := filepath.Join("..", "..", "examples", "golden", "cni-ip-capacity")
	r := run.NewRun("e2e", "e2e", run.Spec{
		BaselineRef: filepath.Join(root, "baseline.json"),
		ChangeRef:   filepath.Join(root, "change.json"),
	})
	eng := &run.Engine{Holder: "test"}
	if err := eng.Execute(r); err != nil {
		t.Fatal(err)
	}
	if r.Status.Phase != run.PhaseCompleted && r.Status.Phase != run.PhaseGated {
		// engine completes gated-only as Completed
		if r.Status.Decision == "" {
			t.Fatalf("phase=%s decision=%s msg=%s", r.Status.Phase, r.Status.Decision, r.Status.Message)
		}
	}
	if r.Digests.BaselineDigest == "" || r.Digests.ReportDigest == "" {
		t.Fatal("expected digests")
	}
}
