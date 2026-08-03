package change_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/justrunme/architecture-rehearsal/internal/change"
	"github.com/justrunme/architecture-rehearsal/internal/loader"
	"github.com/justrunme/architecture-rehearsal/internal/validate"
)

func TestManifestScopeNoMassDelete(t *testing.T) {
	base, err := loader.LoadSnapshot("../../examples/golden/cni-ip-capacity/baseline.json")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	y := `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: payments-api
  namespace: payments
spec:
  replicas: 8
  selector:
    matchLabels: {app: api}
  strategy:
    rollingUpdate:
      maxSurge: 25%
  template:
    metadata:
      labels: {app: api}
    spec:
      containers: [{name: c, image: x}]
`
	_ = os.WriteFile(filepath.Join(dir, "api.yaml"), []byte(y), 0o644)
	ch, err := change.FromManifestsDiff(base, dir, "c1", "scale api", change.ManifestScope{
		Namespaces: []string{"payments"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(ch.RemovedNodes) != 0 {
		t.Fatalf("unexpected removals without allowRemove: %v", ch.RemovedNodes)
	}
	ch2, err := change.FromManifestsDiff(base, dir, "c2", "scale", change.ManifestScope{
		Namespaces:  []string{"payments"},
		AllowRemove: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	foundWorker := false
	for _, id := range ch2.RemovedNodes {
		if id == "workload/payments/worker" {
			foundWorker = true
		}
	}
	if !foundWorker {
		t.Fatalf("expected worker removal within scope, got %v", ch2.RemovedNodes)
	}
}

func TestTerraformSeedsValid(t *testing.T) {
	base, err := loader.LoadSnapshot("../../examples/golden/rwo-node-loss/baseline.json")
	if err != nil {
		t.Fatal(err)
	}
	plan := `{
	  "resource_changes": [{
	    "address": "aws_eks_node_group.main",
	    "type": "aws_eks_node_group",
	    "change": {
	      "actions": ["update"],
	      "before": {"desired_size": 2},
	      "after": {"desired_size": 5}
	    }
	  }]
	}`
	p := filepath.Join(t.TempDir(), "plan.json")
	_ = os.WriteFile(p, []byte(plan), 0o644)
	ch, err := change.FromTerraformPlan(p, "tf1", "scale ng", base)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range ch.Seeds {
		if len(s) >= 3 && s[:3] == "tf:" {
			t.Fatalf("invalid tf: seed %s", s)
		}
	}
	if err := validate.ChangeAgainstBaseline(base, ch); err != nil {
		t.Fatal(err)
	}
}
