package capacity_test

import (
	"fmt"
	"testing"

	"github.com/justrunme/architecture-rehearsal/internal/capacity"
	"github.com/justrunme/architecture-rehearsal/internal/graph"
)

func TestSchedulingEstimate_PrefersClusterPodsOverWorkloads(t *testing.T) {
	snap := &graph.Snapshot{
		Meta: map[string]any{
			// Stale meta that would false-PASS if trusted while pods are present.
			"pod_scheduling_capacity_estimate": 106,
		},
		Nodes: []graph.Node{
			{ID: "node/n1", Kind: graph.KindNode, Attributes: map[string]any{"allocatablePods": 110}},
			{ID: "workload/shop/api", Kind: graph.KindWorkload, Attributes: map[string]any{"replicas": 3}},
		},
	}
	for i := 0; i < 106; i++ {
		snap.Nodes = append(snap.Nodes, graph.Node{
			ID:         fmt.Sprintf("pod/other/f-%d", i),
			Kind:       graph.KindPod,
			Attributes: map[string]any{"phase": "Running"},
		})
	}
	r, err := (capacity.SchedulingEstimate{}).Read(snap)
	if err != nil {
		t.Fatal(err)
	}
	if r.Available != 4 {
		t.Fatalf("want available=4 (110-106), got %d detail=%s", r.Available, r.Detail)
	}
	if r.Source != capacity.SourceSchedulingEstimate {
		t.Fatalf("source=%s", r.Source)
	}
}
