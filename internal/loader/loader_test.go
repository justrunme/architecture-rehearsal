package loader_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/justrunme/architecture-rehearsal/internal/graph"
	"github.com/justrunme/architecture-rehearsal/internal/loader"
)

func TestLoaderAndApplyChange(t *testing.T) {
	dir := t.TempDir()

	snapJSON := `{
		"apiVersion": "rehearsal.io/v1alpha1",
		"kind": "ArchitectureSnapshot",
		"id": "snap-1",
		"name": "snap-1",
		"phase": "baseline",
		"nodes": [
			{"id": "n1", "kind": "Service", "name": "svc-1"}
		]
	}`
	snapPath := filepath.Join(dir, "snap.json")
	if err := os.WriteFile(snapPath, []byte(snapJSON), 0o644); err != nil {
		t.Fatal(err)
	}

	snap, err := loader.LoadSnapshot(snapPath)
	if err != nil {
		t.Fatalf("LoadSnapshot failed: %v", err)
	}
	if snap.ID != "snap-1" {
		t.Fatalf("expected ID snap-1, got %s", snap.ID)
	}

	changeJSON := `{
		"apiVersion": "rehearsal.io/v1alpha1",
		"kind": "rwo-node-loss",
		"id": "ch-1",
		"title": "node loss",
		"facts": {"replicas": 3, "enabled": true, "name": "test"}
	}`
	changePath := filepath.Join(dir, "change.json")
	if err := os.WriteFile(changePath, []byte(changeJSON), 0o644); err != nil {
		t.Fatal(err)
	}

	ch, err := loader.LoadChange(changePath)
	if err != nil {
		t.Fatalf("LoadChange failed: %v", err)
	}
	if ch.ID != "ch-1" {
		t.Fatalf("expected ID ch-1, got %s", ch.ID)
	}

	proposed := loader.ApplyChange(snap, ch)
	if proposed == nil || proposed.Phase != graph.PhaseProposed {
		t.Fatalf("expected proposed snapshot, got %+v", proposed)
	}

	// Test helper facts
	if loader.FactInt(ch.Facts, "replicas", 0) != 3 {
		t.Fatalf("FactInt failed")
	}
	if !loader.FactBool(ch.Facts, "enabled", false) {
		t.Fatalf("FactBool failed")
	}
	if loader.FactString(ch.Facts, "name", "") != "test" {
		t.Fatalf("FactString failed")
	}
}
