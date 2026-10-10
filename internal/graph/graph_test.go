package graph_test

import (
	"testing"

	"github.com/justrunme/architecture-rehearsal/internal/graph"
)

func TestGraphCloneAndMerge(t *testing.T) {
	snap := &graph.Snapshot{
		ID:    "s1",
		Name:  "snap-1",
		Phase: graph.PhaseBaseline,
		Nodes: []graph.Node{
			{ID: "n1", Kind: graph.KindService, Name: "svc-1"},
		},
		Edges: []graph.Edge{
			{From: "n1", To: "n2", Rel: graph.RelDependsOn},
		},
	}

	copied := graph.CloneSnapshot(snap)
	if copied == nil {
		t.Fatal("expected non-nil copy")
	}
	if copied.ID != snap.ID || len(copied.Nodes) != 1 || len(copied.Edges) != 1 {
		t.Fatalf("copy mismatch: %+v", copied)
	}

	// Mutate copy and verify original is unchanged
	copied.Nodes[0].Name = "mutated"
	if snap.Nodes[0].Name == "mutated" {
		t.Fatal("mutation of copy leaked to original snapshot")
	}

	// Test MergeSnapshots
	snap2 := &graph.Snapshot{
		ID:    "s2",
		Name:  "snap-2",
		Phase: graph.PhaseBaseline,
		Nodes: []graph.Node{
			{ID: "n2", Kind: graph.KindService, Name: "svc-2"},
		},
	}
	merged := graph.MergeSnapshots("global", snap, snap2)
	if merged == nil || len(merged.Nodes) < 2 {
		t.Fatalf("expected merged snapshot with >=2 nodes, got %+v", merged)
	}

	// Test BuildIndex and WalkForward
	idx := graph.BuildIndex(merged)
	reachable := graph.WalkForward(idx, []string{merged.Nodes[0].ID}, nil, 5)
	if len(reachable) == 0 {
		t.Fatal("expected reachable nodes")
	}
}
