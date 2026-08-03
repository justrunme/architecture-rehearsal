package collect_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/justrunme/architecture-rehearsal/internal/collect"
)

func TestStrictYAMLDefaultFailsClosed(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "good.yaml"), []byte(`
apiVersion: v1
kind: Node
metadata:
  name: n1
status:
  allocatable:
    pods: "5"
`), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "bad.yaml"), []byte(`: : broken`), 0o644)

	_, err := collect.K8sFromManifests(nil, dir, collect.K8sOptions{ClusterName: "c"})
	if err == nil {
		t.Fatal("expected fail-closed on malformed YAML by default")
	}

	snap, err := collect.K8sFromManifests(nil, dir, collect.K8sOptions{
		ClusterName:  "c",
		AllowPartial: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if snap.Meta["coverage_gap"] != "yaml_parse_errors_present" {
		t.Fatalf("meta=%v", snap.Meta)
	}
	if snap.Meta["pod_scheduling_capacity_estimate"] == nil {
		t.Fatal("expected scheduling capacity estimate")
	}
}
