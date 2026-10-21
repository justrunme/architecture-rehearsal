package collect_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/justrunme/architecture-rehearsal/internal/collect"
	"github.com/justrunme/architecture-rehearsal/internal/graph"
	"github.com/justrunme/architecture-rehearsal/internal/podtemplate"
)

func TestCollect_LiveDefaultsHashMatchesDesired(t *testing.T) {
	// Simulate a live Node/Deployment dump with API-populated defaults (as kubectl
	// get -o yaml would show) versus a lean desired manifest template.
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
      schedulerName: default-scheduler
      terminationGracePeriodSeconds: 30
      securityContext: {}
      containers:
        - name: api
          image: nginx:1.27
          imagePullPolicy: IfNotPresent
          terminationMessagePath: /dev/termination-log
          terminationMessagePolicy: File
          resources: {}
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
	liveHash := wl.AttrString("podTemplateHash")
	if liveHash == "" {
		t.Fatal("collector must set podTemplateHash")
	}
	desiredTpl := map[string]any{
		"metadata": map[string]any{"labels": map[string]any{"app": "api"}},
		"spec": map[string]any{
			"containers": []any{
				map[string]any{"name": "api", "image": "nginx:1.27"},
			},
		},
	}
	desireHash := podtemplate.Hash(desiredTpl)
	if liveHash != desireHash {
		t.Fatalf("live collector hash %s != desired hash %s", liveHash, desireHash)
	}
	if podtemplate.HashChanged(liveHash, desireHash) {
		t.Fatal("HashChanged must be false for live defaults vs desired")
	}
}
