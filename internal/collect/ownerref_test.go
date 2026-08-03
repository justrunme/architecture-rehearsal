package collect_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/justrunme/architecture-rehearsal/internal/collect"
	"github.com/justrunme/architecture-rehearsal/internal/graph"
)

func TestOwnerReferencesPreferredOverPrefix(t *testing.T) {
	dir := t.TempDir()
	y := `
apiVersion: v1
kind: List
items:
  - apiVersion: apps/v1
    kind: Deployment
    metadata:
      name: api
      namespace: payments
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
          containers: [{name: c, image: x}]
  - apiVersion: apps/v1
    kind: ReplicaSet
    metadata:
      name: api-abc123
      namespace: payments
      ownerReferences:
        - apiVersion: apps/v1
          kind: Deployment
          name: api
          controller: true
    spec:
      replicas: 2
  - apiVersion: v1
    kind: Pod
    metadata:
      name: api-abc123-xyz
      namespace: payments
      ownerReferences:
        - apiVersion: apps/v1
          kind: ReplicaSet
          name: api-abc123
          controller: true
    spec:
      nodeName: n1
      containers: [{name: c, image: x}]
    status:
      phase: Running
  - apiVersion: v1
    kind: Node
    metadata:
      name: n1
    status:
      allocatable:
        pods: "10"
`
	_ = os.WriteFile(filepath.Join(dir, "o.yaml"), []byte(y), 0o644)
	snap, err := collect.K8sFromManifests(nil, dir, collect.K8sOptions{ClusterName: "c"})
	if err != nil {
		t.Fatal(err)
	}
	hasOwn, hasRun := false, false
	for _, e := range snap.Edges {
		if e.From == "workload/payments/api" && e.To == "pod/payments/api-abc123-xyz" && e.Rel == graph.RelOwns {
			hasOwn = true
		}
		if e.From == "workload/payments/api" && e.To == "node/n1" && e.Rel == graph.RelRunsOn {
			hasRun = true
		}
	}
	if !hasOwn || !hasRun {
		t.Fatalf("ownerRef edges missing own=%v run=%v edges=%+v", hasOwn, hasRun, snap.Edges)
	}
}

func TestPVZoneFromNodeAffinity(t *testing.T) {
	dir := t.TempDir()
	y := `
apiVersion: v1
kind: PersistentVolume
metadata:
  name: pv1
spec:
  accessModes: ["ReadWriteOnce"]
  nodeAffinity:
    required:
      nodeSelectorTerms:
        - matchExpressions:
            - key: topology.kubernetes.io/zone
              operator: In
              values: ["eu-central-1c"]
`
	_ = os.WriteFile(filepath.Join(dir, "pv.yaml"), []byte(y), 0o644)
	snap, err := collect.K8sFromManifests(nil, dir, collect.K8sOptions{ClusterName: "c"})
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range snap.Nodes {
		if n.ID == "pv/pv1" && n.AttrString("zone") != "eu-central-1c" {
			t.Fatalf("zone=%q", n.AttrString("zone"))
		}
	}
}
