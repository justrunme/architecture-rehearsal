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

func TestRWOCausalNotPlainPending(t *testing.T) {
	pred := &analyze.Report{
		ChangeID: "c1", Risk: analyze.RiskCritical,
		PredictedFailures: []string{"rwo-node-loss"},
		Findings: []scenario.Finding{{
			Scenario: "rwo-node-loss",
			Components: []string{
				"workload/gitops/gitaly",
				"pvc/gitops/gitaly-data",
			},
		}},
	}
	// Plain pending without PVC/attach — must NOT confirm RWO
	obsWeak := &graph.Snapshot{
		ID: "obs",
		Nodes: []graph.Node{
			{ID: "workload/gitops/gitaly", Kind: graph.KindWorkload, Name: "gitaly", Namespace: "gitops",
				Attributes: map[string]any{"replicas": 1}},
			{ID: "pod/gitops/gitaly-0", Kind: graph.KindPod, Name: "gitaly-0", Namespace: "gitops",
				Attributes: map[string]any{"phase": "Pending"}},
		},
	}
	res := verify.Run(pred, obsWeak)
	for _, c := range res.Checks {
		if c.Name == "scenario:rwo-node-loss" && c.Passed {
			t.Fatalf("plain pending must not confirm RWO: %+v", res.Checks)
		}
	}

	// Causal: PVC boundNode gone + attach failure on pod
	obs := &graph.Snapshot{
		ID: "obs", Phase: graph.PhaseObserved,
		Nodes: []graph.Node{
			{ID: "node/other", Kind: graph.KindNode, Name: "other", Attributes: map[string]any{"zone": "b"}},
			{ID: "workload/gitops/gitaly", Kind: graph.KindWorkload, Name: "gitaly", Namespace: "gitops",
				Attributes: map[string]any{"replicas": 1, "phase": "Pending"}},
			{ID: "pvc/gitops/gitaly-data", Kind: graph.KindPVC, Name: "gitaly-data", Namespace: "gitops",
				Attributes: map[string]any{"accessMode": "ReadWriteOnce", "boundNode": "lost-node", "zone": "a"}},
			{ID: "pod/gitops/gitaly-0", Kind: graph.KindPod, Name: "gitaly-0", Namespace: "gitops",
				Attributes: map[string]any{"phase": "Pending", "reason": "FailedAttachVolume", "message": "Multi-Attach error for volume"}},
		},
		Edges: []graph.Edge{{From: "workload/gitops/gitaly", To: "pvc/gitops/gitaly-data", Rel: graph.RelBindsVolume}},
	}
	ch := &loader.ChangeEnvelope{ID: "c1", RemovedNodes: []string{"node/lost-node"}}
	base := &graph.Snapshot{ID: "b", Nodes: []graph.Node{{ID: "node/lost-node", Kind: graph.KindNode, Name: "lost-node"}}}
	res = verify.RunWithOptions(pred, obs, verify.Options{Baseline: base, Change: ch})
	if res.Outcome != verify.OutcomeVerified {
		t.Fatalf("outcome=%s checks=%+v", res.Outcome, res.Checks)
	}
}

func TestCNIRequiresCausalSignal(t *testing.T) {
	pred := &analyze.Report{
		ChangeID: "cni1", Risk: analyze.RiskCritical,
		PredictedFailures: []string{"cni-ip-capacity"},
		Findings: []scenario.Finding{{
			Scenario: "cni-ip-capacity", Components: []string{"workload/payments/payments-api"},
		}},
	}
	// Unrelated pending without CNI reason — no confirm
	obs := &graph.Snapshot{
		ID: "obs",
		Nodes: []graph.Node{
			{ID: "workload/payments/payments-api", Kind: graph.KindWorkload, Name: "payments-api", Namespace: "payments",
				Attributes: map[string]any{"replicas": 18}},
			{ID: "pod/payments/payments-api-x-new", Kind: graph.KindPod, Name: "payments-api-x-new", Namespace: "payments",
				Attributes: map[string]any{"phase": "Pending", "reason": "Unschedulable", "message": "0/2 nodes insufficient cpu"}},
		},
	}
	base := &graph.Snapshot{ID: "b", Nodes: []graph.Node{
		{ID: "workload/payments/payments-api", Kind: graph.KindWorkload, Name: "payments-api", Namespace: "payments",
			Attributes: map[string]any{"replicas": 6}},
	}}
	ch := &loader.ChangeEnvelope{ID: "cni1", PatchNodes: []graph.Node{
		{ID: "workload/payments/payments-api", Attributes: map[string]any{"replicas": 18}},
	}}
	res := verify.RunWithOptions(pred, obs, verify.Options{Baseline: base, Change: ch})
	for _, c := range res.Checks {
		if c.Name == "scenario:cni-ip-capacity" && c.Passed {
			t.Fatalf("CPU pending must not confirm CNI: %+v", res.Checks)
		}
	}

	// Causal CNI
	obs.Nodes[1].Attributes["reason"] = "FailedCreatePodSandBox"
	obs.Nodes[1].Attributes["message"] = "failed to assign an IP address: no free IPs"
	res = verify.RunWithOptions(pred, obs, verify.Options{Baseline: base, Change: ch})
	if res.Outcome != verify.OutcomeVerified {
		t.Fatalf("outcome=%s checks=%+v", res.Outcome, res.Checks)
	}
}

func TestPVCPendingDoesNotConfirmCNI(t *testing.T) {
	pred := &analyze.Report{
		ChangeID: "x", PredictedFailures: []string{"cni-ip-capacity"},
		Findings: []scenario.Finding{{Scenario: "cni-ip-capacity", Components: []string{"workload/gitops/gitaly"}}},
	}
	obs := &graph.Snapshot{
		ID: "obs",
		Nodes: []graph.Node{
			{ID: "workload/gitops/gitaly", Kind: graph.KindWorkload, Name: "gitaly", Namespace: "gitops"},
			{ID: "pvc/gitops/gitaly-data", Kind: graph.KindPVC, Namespace: "gitops",
				Attributes: map[string]any{"accessMode": "ReadWriteOnce", "boundNode": "gone"}},
			{ID: "pod/gitops/gitaly-0", Kind: graph.KindPod, Name: "gitaly-0", Namespace: "gitops",
				Attributes: map[string]any{"phase": "Pending", "reason": "FailedAttachVolume"}},
		},
	}
	res := verify.Run(pred, obs)
	for _, c := range res.Checks {
		if c.Name == "scenario:cni-ip-capacity" && c.Passed {
			t.Fatal("PVC attach pending must not confirm CNI")
		}
	}
}

func TestCNIPendingDoesNotConfirmRWO(t *testing.T) {
	pred := &analyze.Report{
		ChangeID: "x", PredictedFailures: []string{"rwo-node-loss"},
		Findings: []scenario.Finding{{Scenario: "rwo-node-loss", Components: []string{"workload/payments/api"}}},
	}
	obs := &graph.Snapshot{
		ID: "obs",
		Nodes: []graph.Node{
			{ID: "workload/payments/api", Kind: graph.KindWorkload, Name: "api", Namespace: "payments"},
			{ID: "pod/payments/p1", Kind: graph.KindPod, Name: "p1", Namespace: "payments",
				Attributes: map[string]any{"phase": "Pending", "reason": "FailedCreatePodSandBox", "message": "failed to assign an IP"}},
		},
	}
	res := verify.Run(pred, obs)
	for _, c := range res.Checks {
		if c.Name == "scenario:rwo-node-loss" && c.Passed {
			t.Fatal("CNI pending must not confirm RWO without PVC evidence")
		}
	}
}

func TestUnrelatedPendingDoesNotConfirmPDB(t *testing.T) {
	pred := &analyze.Report{
		ChangeID: "x", PredictedFailures: []string{"pdb-disruption"},
		Findings: []scenario.Finding{{
			Scenario: "pdb-disruption",
			// no pdb component, only unrelated workload
			Components: []string{"workload/other/app"},
		}},
	}
	obs := &graph.Snapshot{
		ID: "obs",
		Nodes: []graph.Node{
			{ID: "workload/other/app", Kind: graph.KindWorkload, Name: "app", Namespace: "other",
				Attributes: map[string]any{"phase": "Pending"}},
			{ID: "pod/other/app-1", Kind: graph.KindPod, Name: "app-1", Namespace: "other",
				Attributes: map[string]any{"phase": "Pending"}},
		},
	}
	res := verify.Run(pred, obs)
	for _, c := range res.Checks {
		if c.Name == "scenario:pdb-disruption" && c.Passed {
			t.Fatal("unrelated pending without PDB must not confirm pdb-disruption")
		}
	}
}

func TestUndeployedReplicaPatchFailsIdentity(t *testing.T) {
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
		ChangeID: "scale", Risk: analyze.RiskCritical,
		PredictedFailures: []string{"cni-ip-capacity"},
		Findings: []scenario.Finding{{
			Scenario: "cni-ip-capacity", Components: []string{"workload/payments/api"},
		}},
	}
	// Still at 6 — change not applied
	obs := &graph.Snapshot{
		ID: "obs",
		Meta: map[string]any{"cni_ip_available": 0},
		Nodes: []graph.Node{
			{ID: "workload/payments/api", Kind: graph.KindWorkload, Name: "api", Namespace: "payments",
				Attributes: map[string]any{"replicas": 6}},
		},
	}
	res := verify.RunWithOptions(pred, obs, verify.Options{Baseline: base, Change: ch})
	if res.Outcome == verify.OutcomeVerified {
		t.Fatalf("undeployed patch must not verify: %+v", res.Checks)
	}
	found := false
	for _, c := range res.Checks {
		if strings.HasPrefix(c.Name, "change_applied:") && !c.Passed {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected change_applied failure: %+v", res.Checks)
	}
}

func TestPromMatchCountMustBeZero(t *testing.T) {
	pred := &analyze.Report{
		ChangeID: "p", PredictedFailures: []string{"prom-zero-match"},
	}
	base := &graph.Snapshot{ID: "b"}
	ch := &loader.ChangeEnvelope{ID: "p"}
	obs := &graph.Snapshot{
		ID: "obs",
		Meta: map[string]any{"metric_match_count": 100},
	}
	res := verify.RunWithOptions(pred, obs, verify.Options{Baseline: base, Change: ch})
	for _, c := range res.Checks {
		if c.Name == "scenario:prom-zero-match" && c.Passed {
			t.Fatal("metric_match_count=100 must not pass prom-zero-match")
		}
	}
	obs.Meta["metric_match_count"] = 0
	res = verify.RunWithOptions(pred, obs, verify.Options{Baseline: base, Change: ch})
	pass := false
	for _, c := range res.Checks {
		if c.Name == "scenario:prom-zero-match" && c.Passed {
			pass = true
		}
	}
	if !pass {
		t.Fatalf("metric_match_count=0 should pass: %+v", res.Checks)
	}
}

func TestLegacyWithoutBaselineCapsInconclusive(t *testing.T) {
	pred := &analyze.Report{
		ChangeID: "c", PredictedFailures: []string{"cni-ip-capacity"},
		Findings: []scenario.Finding{{Scenario: "cni-ip-capacity", Components: []string{"workload/payments/api"}}},
	}
	obs := &graph.Snapshot{
		ID: "obs",
		Meta: map[string]any{"cni_ip_available": 0},
		Nodes: []graph.Node{
			{ID: "workload/payments/api", Kind: graph.KindWorkload, Name: "api", Namespace: "payments"},
		},
	}
	res := verify.Run(pred, obs)
	if res.Outcome != verify.OutcomeInconclusive {
		t.Fatalf("legacy max INCONCLUSIVE, got %s checks=%+v", res.Outcome, res.Checks)
	}
	if res.Mode != "legacy" {
		t.Fatalf("mode=%s", res.Mode)
	}
}

func TestChangeIdentityAppliedAndConverged(t *testing.T) {
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
		ChangeID: "scale", Risk: analyze.RiskCritical,
		PredictedFailures: []string{"cni-ip-capacity"},
		Findings: []scenario.Finding{{
			Scenario: "cni-ip-capacity", Components: []string{"workload/payments/api"},
		}},
	}
	obs := &graph.Snapshot{
		ID: "obs",
		Meta: map[string]any{"cni_ip_available": 0},
		Nodes: []graph.Node{
			{ID: "workload/payments/api", Kind: graph.KindWorkload, Name: "api", Namespace: "payments",
				Attributes: map[string]any{"replicas": 18, "readyReplicas": 6}},
		},
	}
	res := verify.RunWithOptions(pred, obs, verify.Options{Baseline: base, Change: ch})
	// change applied but not converged → diverged
	if res.Outcome == verify.OutcomeVerified {
		t.Fatalf("non-converged rollout must not fully verify: %+v", res.Checks)
	}
	obs.Nodes[0].Attributes["readyReplicas"] = 18
	obs.Nodes[0].Attributes["availableReplicas"] = 18
	res = verify.RunWithOptions(pred, obs, verify.Options{Baseline: base, Change: ch})
	if res.Outcome != verify.OutcomeVerified {
		t.Fatalf("outcome=%s checks=%+v", res.Outcome, res.Checks)
	}
}

func TestServiceRoutingEndpointSlice(t *testing.T) {
	pred := &analyze.Report{
		ChangeID: "s", PredictedFailures: []string{"service-routing"},
		Findings: []scenario.Finding{{
			Scenario: "service-routing", Components: []string{"svc/payments/api"},
		}},
	}
	base := &graph.Snapshot{ID: "b"}
	ch := &loader.ChangeEnvelope{ID: "s", RemovedNodes: []string{"workload/payments/api"}}
	obs := &graph.Snapshot{
		ID: "obs",
		Nodes: []graph.Node{
			{ID: "svc/payments/api", Kind: graph.KindService, Name: "api", Namespace: "payments",
				Attributes: map[string]any{"hasEndpointSlice": true, "readyEndpoints": 0}},
		},
	}
	res := verify.RunWithOptions(pred, obs, verify.Options{Baseline: base, Change: ch})
	if res.Outcome != verify.OutcomeVerified {
		t.Fatalf("outcome=%s checks=%+v", res.Outcome, res.Checks)
	}
}

func TestGoldenRWOVerifyIndependent(t *testing.T) {
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
	for _, c := range res.Checks {
		if strings.HasPrefix(c.Name, "component_missing:") && !c.Passed {
			t.Fatalf("unexpected component_missing: %+v", c)
		}
	}
}

func TestOperatorAnnotationIsSoftOnly(t *testing.T) {
	pred := &analyze.Report{
		ChangeID: "c", Risk: analyze.RiskHigh,
		PredictedFailures: []string{"cni-ip-capacity"},
	}
	obs := &graph.Snapshot{
		ID: "obs",
		Meta: map[string]any{"observed_failures": []any{"cni-ip-capacity"}},
		Nodes: []graph.Node{
			{ID: "workload/payments/api", Kind: graph.KindWorkload, Name: "api", Namespace: "payments",
				Attributes: map[string]any{"replicas": 6}},
		},
	}
	base := &graph.Snapshot{ID: "b", Nodes: obs.Nodes}
	ch := &loader.ChangeEnvelope{ID: "c"}
	res := verify.RunWithOptions(pred, obs, verify.Options{Baseline: base, Change: ch})
	if res.Outcome == verify.OutcomeVerified {
		t.Fatalf("annotation alone must not verify: %+v", res.Checks)
	}
}
