package argo_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/justrunme/architecture-rehearsal/internal/argo"
	"github.com/justrunme/architecture-rehearsal/internal/change"
	"github.com/justrunme/architecture-rehearsal/internal/graph"
	"github.com/justrunme/architecture-rehearsal/internal/loader"
)

func TestCompile_FromDesiredDir(t *testing.T) {
	root := filepath.Join("..", "..", "examples", "e2e-argo", "unschedulable")
	base, err := loader.LoadSnapshot(filepath.Join(root, "baseline.json"))
	if err != nil {
		t.Fatal(err)
	}
	ch, err := argo.Compile(base, argo.CompileOptions{
		AppName:    "payments",
		DesiredDir: filepath.Join(root, "desired"),
		Revision:   "abc123",
		Scope:      change.ManifestScope{Namespaces: []string{"demo"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if ch.Kind != "argo-sync" {
		t.Fatalf("kind=%s", ch.Kind)
	}
	if ch.Facts["argo.app"] != "payments" {
		t.Fatalf("facts=%v", ch.Facts)
	}
	if len(ch.Seeds) == 0 && len(ch.PatchNodes) == 0 && len(ch.AddedNodes) == 0 {
		t.Fatal("expected workload scale patch")
	}
}

func TestDirFetcher(t *testing.T) {
	src := filepath.Join("..", "..", "examples", "e2e-argo", "benign", "desired")
	dst := t.TempDir()
	f := &argo.DirFetcher{SourceDir: src}
	if err := f.Fetch(argo.ApplicationRef{Name: "x"}, dst); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dst)
	if len(entries) == 0 {
		t.Fatal("expected copied yaml")
	}
}

func TestCompile_SkipsKubeConfigMapAtomicDirs(t *testing.T) {
	// Simulate a projected ConfigMap volume: app.yaml symlink + ..data real file.
	base := &graph.Snapshot{APIVersion: graph.APIVersionV1Alpha1, Nodes: []graph.Node{
		{ID: "node/n1", Kind: graph.KindNode, Name: "n1", Attributes: map[string]any{"allocatablePods": 4, "schedulable": true}},
		{ID: "workload/demo/api", Kind: graph.KindWorkload, Name: "api", Namespace: "demo",
			Attributes: map[string]any{"replicas": 1, "type": "Deployment"}},
	}}
	dir := t.TempDir()
	realDir := filepath.Join(dir, "..2026_01_01_00_00_00.1")
	if err := os.Mkdir(realDir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := []byte(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: api
  namespace: demo
spec:
  replicas: 8
  strategy:
    rollingUpdate:
      maxSurge: 4
  template:
    spec:
      nodeSelector:
        e2e.rehearsal.io/no-such-node: "true"
      containers: [{name: api, image: x}]
`)
	if err := os.WriteFile(filepath.Join(realDir, "app.yaml"), body, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Base(realDir), filepath.Join(dir, "..data")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("..data", "app.yaml"), filepath.Join(dir, "app.yaml")); err != nil {
		t.Fatal(err)
	}
	ch, err := argo.Compile(base, argo.CompileOptions{
		AppName: "demo", DesiredDir: dir,
		Scope: change.ManifestScope{Namespaces: []string{"demo"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(ch.PatchNodes) != 1 {
		t.Fatalf("expected 1 patchNode (no ConfigMap-volume duplicate), got %d: %#v", len(ch.PatchNodes), ch.PatchNodes)
	}
	ns, _ := ch.PatchNodes[0].Attributes["nodeSelector"].(map[string]any)
	if len(ns) == 0 {
		t.Fatalf("nodeSelector lost from patch: %#v", ch.PatchNodes[0].Attributes)
	}
}

func TestCompileRecordsArgocdCLISource(t *testing.T) {
	base := &graph.Snapshot{APIVersion: graph.APIVersionV1Alpha1, Nodes: []graph.Node{
		{ID: "workload/demo/api", Kind: graph.KindWorkload, Name: "api", Namespace: "demo",
			Attributes: map[string]any{"replicas": 1}},
	}}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "api.yaml"), []byte(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: api
  namespace: demo
spec:
  replicas: 1
  selector:
    matchLabels: {app: api}
  template:
    metadata:
      labels: {app: api}
    spec:
      containers: [{name: api, image: x}]
`), 0o644); err != nil {
		t.Fatal(err)
	}
	ch, err := argo.Compile(base, argo.CompileOptions{
		AppName: "demo", DesiredDir: dir, Source: argo.SourceArgocdCLI,
		Scope: change.ManifestScope{Namespaces: []string{"demo"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if ch.Facts["source"] != argo.SourceArgocdCLI {
		t.Fatalf("source=%v", ch.Facts["source"])
	}
}
