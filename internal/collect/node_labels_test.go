package collect_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/justrunme/architecture-rehearsal/internal/collect"
	"github.com/justrunme/architecture-rehearsal/internal/graph"
)

func TestNodeLabelsAndTaintsCaptured(t *testing.T) {
	dir := t.TempDir()
	yaml := `
apiVersion: v1
kind: List
items:
  - apiVersion: v1
    kind: Node
    metadata:
      name: worker-a
      labels:
        kubernetes.io/os: linux
        node-pool: general
        topology.kubernetes.io/zone: z1
    spec:
      taints:
        - key: dedicated
          value: gpu
          effect: NoSchedule
    status:
      allocatable:
        pods: "110"
  - apiVersion: apps/v1
    kind: Deployment
    metadata:
      name: api
      namespace: shop
    spec:
      replicas: 3
      strategy:
        type: RollingUpdate
        rollingUpdate:
          maxUnavailable: "100%"
          maxSurge: "0"
      selector:
        matchLabels:
          app: api
      template:
        metadata:
          labels:
            app: api
        spec:
          nodeSelector:
            kubernetes.io/os: linux
          containers:
            - name: api
              image: nginx
`
	if err := os.WriteFile(filepath.Join(dir, "list.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	snap, err := collect.K8sFromManifests(nil, dir, collect.K8sOptions{ClusterName: "kind"})
	if err != nil {
		t.Fatal(err)
	}
	var node *graph.Node
	var wl *graph.Node
	for i := range snap.Nodes {
		n := &snap.Nodes[i]
		if n.ID == "node/worker-a" {
			node = n
		}
		if n.ID == "workload/shop/api" {
			wl = n
		}
	}
	if node == nil {
		t.Fatal("node/worker-a missing")
	}
	labels, ok := node.Attributes["labels"].(map[string]any)
	if !ok || labels == nil {
		t.Fatalf("node labels not captured: %#v", node.Attributes)
	}
	if labels["kubernetes.io/os"] != "linux" || labels["node-pool"] != "general" {
		t.Fatalf("unexpected labels: %#v", labels)
	}
	if node.AttrString("zone") != "z1" {
		t.Fatalf("zone=%q", node.AttrString("zone"))
	}
	taints, ok := node.Attributes["taints"].([]any)
	if !ok || len(taints) != 1 {
		t.Fatalf("taints not captured: %#v", node.Attributes["taints"])
	}
	if wl == nil {
		t.Fatal("workload missing")
	}
	if wl.AttrString("maxUnavailable") != "100%" {
		t.Fatalf("maxUnavailable=%q", wl.AttrString("maxUnavailable"))
	}
	if wl.AttrString("maxSurge") != "0" {
		t.Fatalf("maxSurge=%q", wl.AttrString("maxSurge"))
	}
	if wl.AttrString("containerImages") != "api=nginx" {
		t.Fatalf("containerImages=%q", wl.AttrString("containerImages"))
	}
	ns, _ := wl.Attributes["nodeSelector"].(map[string]any)
	if ns["kubernetes.io/os"] != "linux" {
		t.Fatalf("workload nodeSelector not captured: %#v", wl.Attributes["nodeSelector"])
	}
}

func TestNodeLabelsEmptyMapWhenAbsent(t *testing.T) {
	dir := t.TempDir()
	yaml := `
apiVersion: v1
kind: Node
metadata:
  name: bare
status:
  allocatable:
    pods: "10"
`
	if err := os.WriteFile(filepath.Join(dir, "n.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	snap, err := collect.K8sFromManifests(nil, dir, collect.K8sOptions{ClusterName: "c"})
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range snap.Nodes {
		if n.Kind != graph.KindNode {
			continue
		}
		labels, ok := n.Attributes["labels"].(map[string]any)
		if !ok {
			t.Fatalf("expected labels key even when metadata.labels absent, attrs=%#v", n.Attributes)
		}
		if len(labels) != 0 {
			t.Fatalf("expected empty labels map, got %#v", labels)
		}
		return
	}
	t.Fatal("no node")
}
