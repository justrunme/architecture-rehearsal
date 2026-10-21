package collect_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/justrunme/architecture-rehearsal/internal/collect"
	"github.com/justrunme/architecture-rehearsal/internal/graph"
	"github.com/justrunme/architecture-rehearsal/internal/podtemplate"
)

func TestCollect_StoresPodTemplate_SubsetIgnoresDefaults(t *testing.T) {
	dir := t.TempDir()
	live := `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: api
  namespace: shop
spec:
  replicas: 3
  selector:
    matchLabels:
      app: api
  template:
    metadata:
      creationTimestamp: null
      labels:
        app: api
    spec:
      restartPolicy: Always
      dnsPolicy: ClusterFirst
      containers:
        - name: api
          image: nginx:1.27
          imagePullPolicy: IfNotPresent
          readinessProbe:
            httpGet:
              path: /
              port: http
              scheme: HTTP
`
	if err := os.WriteFile(filepath.Join(dir, "live.yaml"), []byte(live), 0o644); err != nil {
		t.Fatal(err)
	}
	snap, err := collect.K8sFromManifests(nil, dir, collect.K8sOptions{ClusterName: "kind"})
	if err != nil {
		t.Fatal(err)
	}
	var wl *graph.Node
	for i := range snap.Nodes {
		if snap.Nodes[i].ID == "workload/shop/api" {
			wl = &snap.Nodes[i]
			break
		}
	}
	if wl == nil {
		t.Fatal("workload missing")
	}
	liveTpl, _ := wl.Attributes["podTemplate"].(map[string]any)
	if liveTpl == nil {
		t.Fatal("collector must store podTemplate")
	}
	desired := map[string]any{
		"metadata": map[string]any{"labels": map[string]any{"app": "api"}},
		"spec": map[string]any{
			"containers": []any{
				map[string]any{
					"name":  "api",
					"image": "nginx:1.27",
					"readinessProbe": map[string]any{
						"httpGet": map[string]any{"path": "/", "port": "http"},
					},
				},
			},
		},
	}
	if podtemplate.SubsetChanged(desired, liveTpl) {
		t.Fatal("desired without server defaults must subset-equal live template")
	}
}
