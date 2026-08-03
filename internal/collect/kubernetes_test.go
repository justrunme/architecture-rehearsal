package collect_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/justrunme/architecture-rehearsal/internal/collect"
	"github.com/justrunme/architecture-rehearsal/internal/graph"
)

func TestK8sListAndEdges(t *testing.T) {
	dir := t.TempDir()
	// recursive subdir
	sub := filepath.Join(dir, "apps")
	_ = os.MkdirAll(sub, 0o755)
	yaml := `
apiVersion: v1
kind: List
items:
  - apiVersion: v1
    kind: Node
    metadata:
      name: n1
      labels:
        topology.kubernetes.io/zone: eu-central-1a
    status:
      allocatable:
        pods: "10"
  - apiVersion: apps/v1
    kind: StatefulSet
    metadata:
      name: gitaly
      namespace: gitops
    spec:
      replicas: 1
      selector:
        matchLabels:
          app: gitaly
      template:
        metadata:
          labels:
            app: gitaly
        spec:
          serviceAccountName: gitaly
          volumes:
            - name: data
              persistentVolumeClaim:
                claimName: gitaly-data
  - apiVersion: v1
    kind: PersistentVolumeClaim
    metadata:
      name: gitaly-data
      namespace: gitops
      annotations:
        volume.kubernetes.io/selected-node: n1
    spec:
      accessModes: ["ReadWriteOnce"]
      volumeName: pv-gitaly
  - apiVersion: v1
    kind: PersistentVolume
    metadata:
      name: pv-gitaly
      labels:
        topology.kubernetes.io/zone: eu-central-1a
    spec:
      accessModes: ["ReadWriteOnce"]
  - apiVersion: v1
    kind: Service
    metadata:
      name: gitaly
      namespace: gitops
    spec:
      selector:
        app: gitaly
  - apiVersion: policy/v1
    kind: PodDisruptionBudget
    metadata:
      name: gitaly
      namespace: gitops
    spec:
      minAvailable: 1
      selector:
        matchLabels:
          app: gitaly
`
	if err := os.WriteFile(filepath.Join(sub, "list.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	snap, err := collect.K8sFromManifests(nil, dir, collect.K8sOptions{ClusterName: "acme-prod"})
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[graph.Kind]int{}
	for _, n := range snap.Nodes {
		kinds[n.Kind]++
	}
	if kinds[graph.KindWorkload] < 1 || kinds[graph.KindPVC] < 1 || kinds[graph.KindService] < 1 {
		t.Fatalf("kinds=%v nodes=%d", kinds, len(snap.Nodes))
	}
	hasBind := false
	hasRoute := false
	hasPDB := false
	for _, e := range snap.Edges {
		if e.Rel == graph.RelBindsVolume {
			hasBind = true
		}
		if e.Rel == graph.RelRoutesTo {
			hasRoute = true
		}
		if e.Rel == graph.RelProtectedBy {
			hasPDB = true
		}
	}
	if !hasBind {
		t.Fatal("expected Workload→PVC edge")
	}
	if !hasRoute {
		t.Fatal("expected Service→Workload ROUTES_TO")
	}
	if !hasPDB {
		t.Fatal("expected Workload→PDB PROTECTED_BY")
	}
	// PVC zone from PV
	for _, n := range snap.Nodes {
		if n.ID == "pvc/gitops/gitaly-data" && n.AttrString("zone") != "eu-central-1a" {
			t.Fatalf("pvc zone=%q", n.AttrString("zone"))
		}
	}
}

func TestDefaultNamespace(t *testing.T) {
	dir := t.TempDir()
	y := `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: api
spec:
  replicas: 2
  selector:
    matchLabels:
      app: api
  template:
    metadata:
      labels:
        app: api
    spec:
      containers: [{name: c, image: nginx}]
`
	_ = os.WriteFile(filepath.Join(dir, "d.yaml"), []byte(y), 0o644)
	snap, err := collect.K8sFromManifests(nil, dir, collect.K8sOptions{ClusterName: "c"})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, n := range snap.Nodes {
		if n.Kind == graph.KindWorkload && n.Namespace == "default" {
			found = true
		}
	}
	if !found {
		t.Fatal("expected default namespace")
	}
}
