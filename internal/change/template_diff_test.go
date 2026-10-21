package change_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/justrunme/architecture-rehearsal/internal/change"
	"github.com/justrunme/architecture-rehearsal/internal/graph"
)

func TestFromManifestsDiff_EnvOnlyNotEmpty(t *testing.T) {
	base := &graph.Snapshot{
		ID: "t", Phase: graph.PhaseBaseline,
		Nodes: []graph.Node{{
			ID: "workload/shop/api", Kind: graph.KindWorkload, Name: "api", Namespace: "shop",
			Attributes: map[string]any{
				"replicas": 3, "type": "Deployment",
				"maxUnavailable":  "100%",
				"maxSurge":        "0",
				"containerImages": "api=nginx:1.27",
			},
		}},
	}
	dir := t.TempDir()
	y := `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: api
  namespace: shop
spec:
  replicas: 3
  strategy:
    rollingUpdate:
      maxUnavailable: 100%
      maxSurge: 0
  selector:
    matchLabels: {app: api}
  template:
    metadata:
      labels: {app: api}
    spec:
      containers:
        - name: api
          image: nginx:1.27
          env:
            - name: FEATURE_FLAGS
              value: "fast-checkout=on"
`
	if err := os.WriteFile(filepath.Join(dir, "d.yaml"), []byte(y), 0o644); err != nil {
		t.Fatal(err)
	}
	ch, err := change.FromManifestsDiff(base, dir, "env", "env change", change.ManifestScope{})
	if err != nil {
		t.Fatal(err)
	}
	if len(ch.PatchNodes) == 0 || len(ch.Seeds) == 0 {
		t.Fatalf("env-only template change must not be empty change: patches=%d seeds=%v", len(ch.PatchNodes), ch.Seeds)
	}
	if ch.PatchNodes[0].Attributes["podTemplateChanged"] != true {
		t.Fatalf("podTemplateChanged=%v", ch.PatchNodes[0].Attributes["podTemplateChanged"])
	}
	if ch.PatchNodes[0].AttrString("containerEnv") == "" {
		t.Fatal("expected containerEnv fingerprint on patch")
	}
}

func TestFromManifestsDiff_UnchangedTemplateNoPatch(t *testing.T) {
	base := &graph.Snapshot{
		ID: "t", Phase: graph.PhaseBaseline,
		Nodes: []graph.Node{{
			ID: "workload/shop/api", Kind: graph.KindWorkload, Name: "api", Namespace: "shop",
			Attributes: map[string]any{
				"replicas": 3, "type": "Deployment",
				"maxUnavailable":  "100%",
				"maxSurge":        "0",
				"containerImages": "api=nginx:1.27",
			},
		}},
	}
	dir := t.TempDir()
	y := `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: api
  namespace: shop
spec:
  replicas: 3
  strategy:
    rollingUpdate:
      maxUnavailable: 100%
      maxSurge: 0
  selector:
    matchLabels: {app: api}
  template:
    metadata:
      labels: {app: api}
    spec:
      containers:
        - name: api
          image: nginx:1.27
`
	if err := os.WriteFile(filepath.Join(dir, "d.yaml"), []byte(y), 0o644); err != nil {
		t.Fatal(err)
	}
	ch, err := change.FromManifestsDiff(base, dir, "noop", "noop", change.ManifestScope{})
	if err != nil {
		t.Fatal(err)
	}
	if len(ch.PatchNodes) != 0 {
		t.Fatalf("unchanged template+strategy must be empty, got %#v", ch.PatchNodes)
	}
}
