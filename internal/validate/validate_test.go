package validate_test

import (
	"testing"

	"github.com/justrunme/architecture-rehearsal/internal/graph"
	"github.com/justrunme/architecture-rehearsal/internal/loader"
	"github.com/justrunme/architecture-rehearsal/internal/validate"
)

func TestSnapshotAndChangeValidation(t *testing.T) {
	// Nil snapshot
	if err := validate.Snapshot(nil); err == nil {
		t.Fatal("expected error for nil snapshot")
	}

	// Valid snapshot
	snap := &graph.Snapshot{
		ID:    "s1",
		Phase: graph.PhaseBaseline,
		Nodes: []graph.Node{
			{ID: "n1", Kind: graph.KindService, Name: "s1"},
			{ID: "n2", Kind: graph.KindPod, Name: "p1"},
		},
		Edges: []graph.Edge{
			{From: "n1", To: "n2", Rel: graph.RelDependsOn},
		},
	}
	if err := validate.Snapshot(snap); err != nil {
		t.Fatalf("unexpected validation error for valid snapshot: %v", err)
	}

	// Unknown kind
	badKind := &graph.Snapshot{
		ID:    "s2",
		Nodes: []graph.Node{{ID: "n1", Kind: "InvalidKind"}},
	}
	if err := validate.Snapshot(badKind); err == nil {
		t.Fatal("expected error for unknown node kind")
	}

	// Valid change
	ch := &loader.ChangeEnvelope{
		ID:    "ch-1",
		Title: "test change",
	}
	if err := validate.Change(ch); err != nil {
		t.Fatalf("unexpected change validation error: %v", err)
	}

	// Change against baseline
	if err := validate.ChangeAgainstBaseline(snap, ch); err != nil {
		t.Fatalf("unexpected error validating change against baseline: %v", err)
	}
}
