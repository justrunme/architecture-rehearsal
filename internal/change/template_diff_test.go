package change_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/justrunme/architecture-rehearsal/internal/change"
	"github.com/justrunme/architecture-rehearsal/internal/graph"
	"github.com/justrunme/architecture-rehearsal/internal/podtemplate"
)

func TestFromManifestsDiff_EnvOnlyNotEmpty(t *testing.T) {
	baseTpl := map[string]any{
		"metadata": map[string]any{"labels": map[string]any{"app": "api"}},
		"spec": map[string]any{
			"containers": []any{
				map[string]any{"name": "api", "image": "nginx:1.27"},
			},
		},
	}
	base := &graph.Snapshot{
		ID: "t", Phase: graph.PhaseBaseline,
		Nodes: []graph.Node{{
			ID: "workload/shop/api", Kind: graph.KindWorkload, Name: "api", Namespace: "shop",
			Attributes: map[string]any{
				"replicas": 3, "type": "Deployment",
				"maxUnavailable":  "100%",
				"maxSurge":        "0",
				"podTemplateHash": podtemplate.Hash(baseTpl),
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
	if ch.PatchNodes[0].AttrString("podTemplateHash") == "" {
		t.Fatal("expected podTemplateHash on patch")
	}
	if ch.PatchNodes[0].AttrString("podTemplateHash") == base.Nodes[0].AttrString("podTemplateHash") {
		t.Fatal("env change must produce a different podTemplateHash")
	}
}

func TestFromManifestsDiff_ResourcesOnlyNotEmpty(t *testing.T) {
	baseTpl := map[string]any{
		"metadata": map[string]any{"labels": map[string]any{"app": "api"}},
		"spec": map[string]any{
			"containers": []any{
				map[string]any{"name": "api", "image": "nginx:1.27"},
			},
		},
	}
	base := &graph.Snapshot{
		ID: "t", Phase: graph.PhaseBaseline,
		Nodes: []graph.Node{{
			ID: "workload/shop/api", Kind: graph.KindWorkload, Name: "api", Namespace: "shop",
			Attributes: map[string]any{
				"replicas": 3, "type": "Deployment",
				"maxUnavailable":  "100%",
				"maxSurge":        "0",
				"podTemplateHash": podtemplate.Hash(baseTpl),
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
`
	if err := os.WriteFile(filepath.Join(dir, "d.yaml"), []byte(y), 0o644); err != nil {
		t.Fatal(err)
	}
	ch, err := change.FromManifestsDiff(base, dir, "res", "resources", change.ManifestScope{})
	if err != nil {
		t.Fatal(err)
	}
	if len(ch.PatchNodes) == 0 {
		t.Fatal("resources-only change must produce a patch")
	}
	if ch.PatchNodes[0].Attributes["podTemplateChanged"] != true {
		t.Fatal("resources-only must set podTemplateChanged")
	}
}

func TestFromManifestsDiff_UnchangedTemplateNoPatch(t *testing.T) {
	baseTpl := map[string]any{
		"metadata": map[string]any{"labels": map[string]any{"app": "api"}},
		"spec": map[string]any{
			"containers": []any{
				map[string]any{"name": "api", "image": "nginx:1.27"},
			},
		},
	}
	base := &graph.Snapshot{
		ID: "t", Phase: graph.PhaseBaseline,
		Nodes: []graph.Node{{
			ID: "workload/shop/api", Kind: graph.KindWorkload, Name: "api", Namespace: "shop",
			Attributes: map[string]any{
				"replicas": 3, "type": "Deployment",
				"maxUnavailable":  "100%",
				"maxSurge":        "0",
				"podTemplateHash": podtemplate.Hash(baseTpl),
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

func TestFromManifestsDiff_LiveDefaultsMatchDesiredHash(t *testing.T) {
	desiredTpl := map[string]any{
		"metadata": map[string]any{"labels": map[string]any{"app": "api"}},
		"spec": map[string]any{
			"containers": []any{
				map[string]any{"name": "api", "image": "nginx:1.27"},
			},
		},
	}
	liveTpl := map[string]any{
		"metadata": map[string]any{
			"creationTimestamp": nil,
			"labels":            map[string]any{"app": "api"},
		},
		"spec": map[string]any{
			"restartPolicy":                 "Always",
			"dnsPolicy":                     "ClusterFirst",
			"schedulerName":                 "default-scheduler",
			"terminationGracePeriodSeconds": float64(30),
			"containers": []any{
				map[string]any{
					"name":                     "api",
					"image":                    "nginx:1.27",
					"imagePullPolicy":          "IfNotPresent",
					"terminationMessagePath":   "/dev/termination-log",
					"terminationMessagePolicy": "File",
					"resources":                map[string]any{},
				},
			},
		},
	}
	base := &graph.Snapshot{
		ID: "t", Phase: graph.PhaseBaseline,
		Nodes: []graph.Node{{
			ID: "workload/shop/api", Kind: graph.KindWorkload, Name: "api", Namespace: "shop",
			Attributes: map[string]any{
				"replicas": 3, "type": "Deployment",
				"maxUnavailable":  "100%",
				"maxSurge":        "0",
				"podTemplateHash": podtemplate.Hash(liveTpl),
			},
		}},
	}
	_ = desiredTpl
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
	ch, err := change.FromManifestsDiff(base, dir, "defaults", "defaults", change.ManifestScope{})
	if err != nil {
		t.Fatal(err)
	}
	if len(ch.PatchNodes) != 0 {
		t.Fatalf("live defaults vs same desired must not patch template; got %#v", ch.PatchNodes)
	}
}
