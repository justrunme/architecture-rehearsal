package analyze_test

import (
	"testing"

	"github.com/justrunme/architecture-rehearsal/internal/analyze"
	"github.com/justrunme/architecture-rehearsal/internal/graph"
	"github.com/justrunme/architecture-rehearsal/internal/loader"
)

func TestCoverageGapNeverApproves(t *testing.T) {
	base := &graph.Snapshot{
		ID:    "b",
		Phase: graph.PhaseBaseline,
		Nodes: []graph.Node{
			{ID: "cluster/c", Kind: graph.KindCluster, Name: "c"},
			{ID: "workload/payments/api", Kind: graph.KindWorkload, Name: "api", Namespace: "payments",
				Attributes: map[string]any{"replicas": 2, "type": "Deployment"}},
		},
		Meta: map[string]any{"pod_ip_capacity_available": 100},
	}
	// Malformed change path: parse errors recorded, empty patches → would have been approve
	ch := &loader.ChangeEnvelope{
		ID:    "bad-yaml",
		Title: "partial render",
		Kind:  "k8s-manifest",
		Facts: map[string]any{
			"coverage_gap":      "yaml_parse_errors_present",
			"yaml_parse_errors": 2,
			"scenario":          "cni-ip-capacity",
		},
	}
	rep, err := analyze.Run(base, ch)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Decision == analyze.DecisionApprove {
		t.Fatalf("coverage_gap must not approve: decision=%s missing=%v", rep.Decision, rep.Coverage.RequiredMissing)
	}
	if rep.Decision != analyze.DecisionUnknown {
		t.Fatalf("want unknown, got %s risk=%s findings=%d", rep.Decision, rep.Risk, len(rep.Findings))
	}
	found := false
	for _, m := range rep.Coverage.RequiredMissing {
		if m == "yaml_parse_errors" || m == "coverage_gap:yaml_parse_errors_present" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected yaml_parse_errors in RequiredMissing: %v", rep.Coverage.RequiredMissing)
	}
}

func TestBaselineCoverageGapUnknown(t *testing.T) {
	base := &graph.Snapshot{
		ID:    "b",
		Phase: graph.PhaseBaseline,
		Nodes: []graph.Node{
			{ID: "cluster/c", Kind: graph.KindCluster, Name: "c"},
			{ID: "workload/ns/w", Kind: graph.KindWorkload, Name: "w", Namespace: "ns",
				Attributes: map[string]any{"replicas": 1}},
		},
		Meta: map[string]any{
			"coverage_gap":       "yaml_parse_errors_present",
			"yaml_parse_errors":  1,
			"pod_ip_capacity_available": 50,
		},
	}
	ch := &loader.ChangeEnvelope{
		ID: "noop", Title: "noop", Kind: "k8s-manifest",
		Facts: map[string]any{"scenario": "cni-ip-capacity"},
	}
	rep, err := analyze.Run(base, ch)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Decision == analyze.DecisionApprove {
		t.Fatalf("baseline coverage gap must not approve: %s", rep.Decision)
	}
}
