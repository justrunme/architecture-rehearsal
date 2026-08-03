package change_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/justrunme/architecture-rehearsal/internal/change"
	"github.com/justrunme/architecture-rehearsal/internal/graph"
)

func TestMalformedYAMLFailsClosed(t *testing.T) {
	base := &graph.Snapshot{
		ID: "b",
		Nodes: []graph.Node{
			{ID: "workload/payments/api", Kind: graph.KindWorkload, Name: "api", Namespace: "payments",
				Attributes: map[string]any{"replicas": 2}},
		},
	}
	dir := t.TempDir()
	// Valid doc + broken doc
	_ = os.WriteFile(filepath.Join(dir, "ok.yaml"), []byte(`
apiVersion: apps/v1
kind: Deployment
metadata:
  name: api
  namespace: payments
spec:
  replicas: 4
  selector:
    matchLabels: {app: api}
  template:
    metadata:
      labels: {app: api}
    spec:
      containers: [{name: c, image: x}]
`), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "bad.yaml"), []byte(`{{{{ not yaml`), 0o644)

	_, err := change.FromManifestsDiff(base, dir, "c1", "t", change.ManifestScope{
		Namespaces: []string{"payments"},
	})
	if err == nil {
		t.Fatal("expected fail-closed error on malformed YAML")
	}

	// Opt-in partial still records coverage_gap
	ch, err := change.FromManifestsDiff(base, dir, "c2", "t", change.ManifestScope{
		Namespaces:   []string{"payments"},
		AllowPartial: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if ch.Facts["coverage_gap"] != "yaml_parse_errors_present" {
		t.Fatalf("facts=%v", ch.Facts)
	}
}

func TestUnsupportedWorkloadIgnoredNotCrash(t *testing.T) {
	base := &graph.Snapshot{
		ID: "b",
		Nodes: []graph.Node{
			{ID: "workload/payments/api", Kind: graph.KindWorkload, Name: "api", Namespace: "payments",
				Attributes: map[string]any{"replicas": 1}},
		},
	}
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "job.yaml"), []byte(`
apiVersion: batch/v1
kind: Job
metadata:
  name: migrate
  namespace: payments
spec:
  template:
    spec:
      containers: [{name: c, image: x}]
      restartPolicy: Never
`), 0o644)
	ch, err := change.FromManifestsDiff(base, dir, "c", "t", change.ManifestScope{Namespaces: []string{"payments"}})
	if err != nil {
		t.Fatal(err)
	}
	// Job is not a tracked workload type — empty patches OK (no crash)
	if len(ch.PatchNodes) != 0 {
		t.Fatalf("unexpected patches for Job: %v", ch.PatchNodes)
	}
}
