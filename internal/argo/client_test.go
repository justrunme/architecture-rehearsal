package argo_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/justrunme/architecture-rehearsal/internal/argo"
)

func TestCLIFetcherUsesNamespacedAppArg(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "argocd")
	script := `#!/bin/sh
# record argv
printf '%s\n' "$@" > "` + dir + `/args.txt"
# emit a tiny Deployment YAML on stdout
cat <<'YAML'
apiVersion: apps/v1
kind: Deployment
metadata:
  name: guestbook-ui
  namespace: demo
spec:
  replicas: 1
YAML
`
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	f := &argo.CLIFetcher{Binary: fake, Server: "localhost:8080", Insecure: true, ExtraArgs: []string{"--grpc-web"}}
	dst := filepath.Join(dir, "out")
	if err := f.Fetch(argo.ApplicationRef{Name: "demo-benign", Namespace: "argocd"}, dst); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "args.txt"))
	if err != nil {
		t.Fatal(err)
	}
	got := string(raw)
	if !strings.Contains(got, "app\nmanifests\nargocd/demo-benign\n") && !strings.Contains(got, "argocd/demo-benign") {
		t.Fatalf("expected ns/name app arg, got:\n%s", got)
	}
	if strings.Contains(got, "--app-namespace") {
		t.Fatalf("must not pass --app-namespace; got:\n%s", got)
	}
	if _, err := os.Stat(filepath.Join(dst, "manifests.yaml")); err != nil {
		t.Fatalf("manifests.yaml missing: %v", err)
	}
}

func TestNewCLIFetcherFromEnvGrpcWebAndToken(t *testing.T) {
	t.Setenv("ARGOCD_BINARY", "argocd")
	t.Setenv("ARGOCD_SERVER", "localhost:8080")
	t.Setenv("ARGOCD_AUTH_TOKEN", "tok")
	t.Setenv("ARGOCD_INSECURE", "true")
	t.Setenv("ARGOCD_GRPC_WEB", "true")
	f := argo.NewCLIFetcherFromEnv()
	if f.AuthToken != "tok" {
		t.Fatalf("AuthToken=%q", f.AuthToken)
	}
	if !f.Insecure {
		t.Fatal("expected Insecure")
	}
	found := false
	for _, a := range f.ExtraArgs {
		if a == "--grpc-web" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected --grpc-web in ExtraArgs: %#v", f.ExtraArgs)
	}
}
