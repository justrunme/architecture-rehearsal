package change_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/justrunme/architecture-rehearsal/internal/change"
	"github.com/justrunme/architecture-rehearsal/internal/graph"
	"github.com/justrunme/architecture-rehearsal/internal/podtemplate"
)

func liveTpl() map[string]any {
	return map[string]any{
		"metadata": map[string]any{"labels": map[string]any{"app": "api"}},
		"spec": map[string]any{
			"restartPolicy": "Always",
			"containers": []any{
				map[string]any{
					"name":            "api",
					"image":           "nginx:1.27",
					"imagePullPolicy": "IfNotPresent",
					"readinessProbe": map[string]any{
						"httpGet": map[string]any{"path": "/", "port": "http", "scheme": "HTTP"},
					},
				},
			},
		},
	}
}

func TestFromManifestsDiff_EnvOnlyNotEmpty(t *testing.T) {
	base := &graph.Snapshot{
		ID: "t", Phase: graph.PhaseBaseline,
		Nodes: []graph.Node{{
			ID: "workload/shop/api", Kind: graph.KindWorkload, Name: "api", Namespace: "shop",
			Attributes: map[string]any{
				"replicas": 3, "type": "Deployment",
				"maxUnavailable": "100%", "maxSurge": "0",
				"podTemplate": liveTpl(),
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
          readinessProbe:
            httpGet:
              path: /
              port: http
`
	if err := os.WriteFile(filepath.Join(dir, "d.yaml"), []byte(y), 0o644); err != nil {
		t.Fatal(err)
	}
	ch, err := change.FromManifestsDiff(base, dir, "env", "env change", change.ManifestScope{})
	if err != nil {
		t.Fatal(err)
	}
	if len(ch.PatchNodes) == 0 {
		t.Fatal("env-only template change must produce a patch")
	}
	if ch.PatchNodes[0].Attributes["podTemplateChanged"] != true {
		t.Fatalf("podTemplateChanged=%v", ch.PatchNodes[0].Attributes["podTemplateChanged"])
	}
}

func TestFromManifestsDiff_ResourcesOnlyNotEmpty(t *testing.T) {
	base := &graph.Snapshot{
		ID: "t", Phase: graph.PhaseBaseline,
		Nodes: []graph.Node{{
			ID: "workload/shop/api", Kind: graph.KindWorkload, Name: "api", Namespace: "shop",
			Attributes: map[string]any{
				"replicas": 3, "type": "Deployment",
				"maxUnavailable": "100%", "maxSurge": "0",
				"podTemplate": liveTpl(),
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
          resources:
            requests:
              cpu: 100m
          readinessProbe:
            httpGet:
              path: /
              port: http
`
	if err := os.WriteFile(filepath.Join(dir, "d.yaml"), []byte(y), 0o644); err != nil {
		t.Fatal(err)
	}
	ch, err := change.FromManifestsDiff(base, dir, "res", "resources", change.ManifestScope{})
	if err != nil {
		t.Fatal(err)
	}
	if len(ch.PatchNodes) == 0 || ch.PatchNodes[0].Attributes["podTemplateChanged"] != true {
		t.Fatal("resources-only must set podTemplateChanged")
	}
}

func TestFromManifestsDiff_UnchangedIgnoresLiveDefaults(t *testing.T) {
	base := &graph.Snapshot{
		ID: "t", Phase: graph.PhaseBaseline,
		Nodes: []graph.Node{{
			ID: "workload/shop/api", Kind: graph.KindWorkload, Name: "api", Namespace: "shop",
			Attributes: map[string]any{
				"replicas": 3, "type": "Deployment",
				"maxUnavailable": "100%", "maxSurge": "0",
				"podTemplate": liveTpl(),
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
          readinessProbe:
            httpGet:
              path: /
              port: http
`
	if err := os.WriteFile(filepath.Join(dir, "d.yaml"), []byte(y), 0o644); err != nil {
		t.Fatal(err)
	}
	ch, err := change.FromManifestsDiff(base, dir, "noop", "noop", change.ManifestScope{})
	if err != nil {
		t.Fatal(err)
	}
	if len(ch.PatchNodes) != 0 {
		t.Fatalf("identical desired vs live-with-defaults must be empty, got %#v", ch.PatchNodes)
	}
	_ = podtemplate.SubsetChanged
}
