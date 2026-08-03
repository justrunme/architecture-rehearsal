package verify_test

import (
	"strings"
	"testing"

	"github.com/justrunme/architecture-rehearsal/internal/analyze"
	"github.com/justrunme/architecture-rehearsal/internal/graph"
	"github.com/justrunme/architecture-rehearsal/internal/loader"
	"github.com/justrunme/architecture-rehearsal/internal/scenario"
	"github.com/justrunme/architecture-rehearsal/internal/verify"
)

func TestRWOUsesPendingPodsNotWorkloads(t *testing.T) {
	pred := &analyze.Report{
		ChangeID:          "c1",
		Risk:              analyze.RiskCritical,
		PredictedFailures: []string{"rwo-node-loss"},
		Findings: []scenario.Finding{{
			Scenario:   "rwo-node-loss",
			Components: []string{"workload/gitops/gitaly"},
		}},
	}
	// Pending on Pod only — old bug looked at Workload
	obs := &graph.Snapshot{
		ID: "obs", Phase: graph.PhaseObserved,
		Nodes: []graph.Node{
			{ID: "workload/gitops/gitaly", Kind: graph.KindWorkload, Name: "gitaly", Namespace: "gitops",
				Attributes: map[string]any{"replicas": 1}},
			{ID: "pod/gitops/gitaly-0", Kind: graph.KindPod, Name: "gitaly-0", Namespace: "gitops",
				Attributes: map[string]any{"phase": "Pending", "unschedulable": true}},
		},
	}
	res := verify.Run(pred, obs)
	if res.Outcome != verify.OutcomeVerified {
		t.Fatalf("outcome=%s checks=%+v", res.Outcome, res.Checks)
	}
	found := false
	for _, c := range res.Checks {
		if c.Name == "scenario:rwo-node-loss" && c.Passed {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected RWO pod check pass: %+v", res.Checks)
	}
}

func TestCNIIndependentOfOperatorAnnotation(t *testing.T) {
	pred := &analyze.Report{
		ChangeID:          "cni1",
		Risk:              analyze.RiskCritical,
		PredictedFailures: []string{"cni-ip-capacity"},
		Findings: []scenario.Finding{{
			Scenario:   "cni-ip-capacity",
			Components: []string{"workload/payments/payments-api"},
		}},
	}
	// No meta.observed_failures — must still verify via pending pods
	obs := &graph.Snapshot{
		ID: "obs", Phase: graph.PhaseObserved,
		Nodes: []graph.Node{
			{ID: "workload/payments/payments-api", Kind: graph.KindWorkload, Name: "payments-api", Namespace: "payments",
				Attributes: map[string]any{"replicas": 18}},
			{ID: "pod/payments/x-new", Kind: graph.KindPod, Name: "x-new", Namespace: "payments",
				Attributes: map[string]any{"phase": "Pending"}},
		},
	}
	res := verify.Run(pred, obs)
	if res.Outcome != verify.OutcomeVerified {
		t.Fatalf("outcome=%s summary=%s checks=%+v", res.Outcome, res.Summary, res.Checks)
	}
}

func TestAllPredictionsNeedEvidence(t *testing.T) {
	pred := &analyze.Report{
		ChangeID:          "multi",
		Risk:              analyze.RiskCritical,
		PredictedFailures: []string{"cni-ip-capacity", "rwo-node-loss"},
		Findings: []scenario.Finding{
			{Scenario: "cni-ip-capacity", Components: []string{"workload/payments/api"}},
			{Scenario: "rwo-node-loss", Components: []string{"workload/gitops/gitaly"}},
		},
	}
	// Only CNI evidence (pending), no RWO pending → inconclusive or partial diverge
	obs := &graph.Snapshot{
		ID: "obs",
		Nodes: []graph.Node{
			{ID: "workload/payments/api", Kind: graph.KindWorkload, Name: "api", Namespace: "payments",
				Attributes: map[string]any{"replicas": 10}},
			{ID: "workload/gitops/gitaly", Kind: graph.KindWorkload, Name: "gitaly", Namespace: "gitops"},
			{ID: "pod/payments/p1", Kind: graph.KindPod, Name: "p1", Namespace: "payments",
				Attributes: map[string]any{"phase": "Pending"}},
		},
	}
	res := verify.Run(pred, obs)
	// One scenario unknown → overall inconclusive (no hard fail on unknown-only)
	if res.Outcome == verify.OutcomeVerified {
		t.Fatalf("must not fully verify when RWO evidence missing: %+v", res.Checks)
	}
}

func TestComponentMissingFailsNotInflatesScore(t *testing.T) {
	pred := &analyze.Report{
		ChangeID:          "c",
		Risk:              analyze.RiskHigh,
		PredictedFailures: []string{"cni-ip-capacity"},
		Findings: []scenario.Finding{{
			Scenario:   "cni-ip-capacity",
			Components: []string{"workload/payments/missing"},
		}},
	}
	obs := &graph.Snapshot{
		ID: "obs",
		Nodes: []graph.Node{
			{ID: "pod/payments/p1", Kind: graph.KindPod, Namespace: "payments",
				Attributes: map[string]any{"phase": "Pending"}},
		},
	}
	res := verify.Run(pred, obs)
	if res.Outcome == verify.OutcomeVerified {
		t.Fatalf("missing component must not fully verify: %+v", res)
	}
	hasMissing := false
	for _, c := range res.Checks {
		if c.Name == "component_missing:workload/payments/missing" && !c.Passed {
			hasMissing = true
		}
	}
	if !hasMissing {
		t.Fatalf("expected component_missing check: %+v", res.Checks)
	}
}

func TestOperatorAnnotationIsSoftOnly(t *testing.T) {
	pred := &analyze.Report{
		ChangeID:          "c",
		Risk:              analyze.RiskHigh,
		PredictedFailures: []string{"cni-ip-capacity"},
	}
	// Annotation claims failure but NO independent pending evidence
	obs := &graph.Snapshot{
		ID: "obs",
		Meta: map[string]any{
			"observed_failures": []any{"cni-ip-capacity"},
		},
		Nodes: []graph.Node{
			{ID: "workload/payments/api", Kind: graph.KindWorkload, Name: "api", Namespace: "payments",
				Attributes: map[string]any{"replicas": 6}},
		},
	}
	res := verify.Run(pred, obs)
	if res.Outcome == verify.OutcomeVerified {
		t.Fatalf("annotation alone must not verify: %+v", res.Checks)
	}
}

func TestChangeIdentityAndDelta(t *testing.T) {
	base := &graph.Snapshot{
		ID: "base",
		Nodes: []graph.Node{
			{ID: "workload/payments/api", Kind: graph.KindWorkload, Name: "api", Namespace: "payments",
				Attributes: map[string]any{"replicas": 6}},
		},
	}
	ch := &loader.ChangeEnvelope{
		ID: "scale",
		PatchNodes: []graph.Node{
			{ID: "workload/payments/api", Attributes: map[string]any{"replicas": 18}},
		},
	}
	pred := &analyze.Report{
		ChangeID:          "scale",
		Risk:              analyze.RiskCritical,
		PredictedFailures: []string{"cni-ip-capacity"},
		Findings: []scenario.Finding{{
			Scenario: "cni-ip-capacity", Components: []string{"workload/payments/api"},
		}},
	}
	obs := &graph.Snapshot{
		ID: "obs",
		Nodes: []graph.Node{
			{ID: "workload/payments/api", Kind: graph.KindWorkload, Name: "api", Namespace: "payments",
				Attributes: map[string]any{"replicas": 18}},
			{ID: "pod/payments/p1", Kind: graph.KindPod, Namespace: "payments",
				Attributes: map[string]any{"phase": "Pending"}},
		},
	}
	res := verify.RunWithOptions(pred, obs, verify.Options{Baseline: base, Change: ch})
	if res.Outcome != verify.OutcomeVerified {
		t.Fatalf("outcome=%s checks=%+v", res.Outcome, res.Checks)
	}
	if res.DeployedChangeDigest == "" {
		t.Fatal("expected deployed change digest")
	}
}

func TestGoldenRWOVerifyIndependent(t *testing.T) {
	// Mirrors CI verify loop against golden fixtures after multi-scenario prediction.
	base, err := loader.LoadSnapshot("../../examples/golden/rwo-node-loss/baseline.json")
	if err != nil {
		t.Fatal(err)
	}
	ch, err := loader.LoadChange("../../examples/golden/rwo-node-loss/change.json")
	if err != nil {
		t.Fatal(err)
	}
	rep, err := analyze.Run(base, ch)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Decision != analyze.DecisionBlock {
		t.Fatalf("decision=%s", rep.Decision)
	}
	obs, err := loader.LoadSnapshot("../../examples/golden/rwo-node-loss/observed.json")
	if err != nil {
		t.Fatal(err)
	}
	res := verify.RunWithOptions(rep, obs, verify.Options{Baseline: base, Change: ch})
	if res.Outcome != verify.OutcomeVerified {
		t.Fatalf("outcome=%s summary=%s checks=%+v", res.Outcome, res.Summary, res.Checks)
	}
	// Must not hard-fail on cascade components (svc/pdb/lost node)
	for _, c := range res.Checks {
		if strings.HasPrefix(c.Name, "component_missing:") && !c.Passed {
			t.Fatalf("unexpected component_missing hard fail: %+v", c)
		}
	}
}

func TestNoEvidenceInconclusive(t *testing.T) {
	pred := &analyze.Report{
		ChangeID:          "c",
		PredictedFailures: []string{"cni-ip-capacity"},
	}
	obs := &graph.Snapshot{
		ID: "obs",
		Nodes: []graph.Node{
			{ID: "workload/payments/api", Kind: graph.KindWorkload, Name: "api", Namespace: "payments"},
		},
	}
	res := verify.Run(pred, obs)
	if res.Outcome != verify.OutcomeInconclusive {
		t.Fatalf("outcome=%s want inconclusive checks=%+v", res.Outcome, res.Checks)
	}
}
