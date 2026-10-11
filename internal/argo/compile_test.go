package argo_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/justrunme/architecture-rehearsal/internal/argo"
	"github.com/justrunme/architecture-rehearsal/internal/change"
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
