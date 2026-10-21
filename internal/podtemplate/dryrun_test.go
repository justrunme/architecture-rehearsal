package podtemplate_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/justrunme/architecture-rehearsal/internal/podtemplate"
)

func writeFakeKubectl(t *testing.T, script string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "kubectl")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDryRunTemplate_ClientSideArgs(t *testing.T) {
	script := `#!/bin/sh
echo "$@" > "$(dirname "$0")/args.txt"
cat >/dev/null
cat <<'EOF'
{"apiVersion":"apps/v1","kind":"Deployment","metadata":{"name":"api"},"spec":{"template":{"metadata":{"labels":{"app":"api"}},"spec":{"containers":[{"name":"api","image":"nginx"}]}}}}
EOF
`
	kubectl := writeFakeKubectl(t, script)
	tpl, err := podtemplate.DryRunTemplate(context.Background(), map[string]any{
		"apiVersion": "apps/v1",
		"kind":       "Deployment",
		"metadata":   map[string]any{"name": "api", "namespace": "shop"},
		"spec": map[string]any{
			"template": map[string]any{
				"metadata": map[string]any{"labels": map[string]any{"app": "api"}},
				"spec":     map[string]any{"containers": []any{map[string]any{"name": "api", "image": "nginx"}}},
			},
		},
	}, podtemplate.KubectlOptions{Kubectl: kubectl, Mode: podtemplate.ApplyClientSide})
	if err != nil {
		t.Fatal(err)
	}
	if tpl == nil {
		t.Fatal("expected template")
	}
	args, _ := os.ReadFile(filepath.Join(filepath.Dir(kubectl), "args.txt"))
	s := string(args)
	if strings.Contains(s, "--server-side") {
		t.Fatalf("client-side dry-run must not pass --server-side: %s", s)
	}
	if !strings.Contains(s, "--dry-run=server") {
		t.Fatalf("expected --dry-run=server: %s", s)
	}
}

func TestDryRunTemplate_ServerSideArgs(t *testing.T) {
	script := `#!/bin/sh
echo "$@" > "$(dirname "$0")/args.txt"
cat >/dev/null
cat <<'EOF'
{"apiVersion":"apps/v1","kind":"Deployment","metadata":{"name":"api"},"spec":{"template":{"metadata":{"labels":{"app":"api"}},"spec":{"containers":[{"name":"api","image":"nginx"}]}}}}
EOF
`
	kubectl := writeFakeKubectl(t, script)
	_, err := podtemplate.DryRunTemplate(context.Background(), map[string]any{
		"apiVersion": "apps/v1",
		"kind":       "Deployment",
		"metadata":   map[string]any{"name": "api"},
		"spec": map[string]any{
			"template": map[string]any{
				"spec": map[string]any{"containers": []any{map[string]any{"name": "api", "image": "nginx"}}},
			},
		},
	}, podtemplate.KubectlOptions{Kubectl: kubectl, Mode: podtemplate.ApplyServerSide})
	if err != nil {
		t.Fatal(err)
	}
	args, _ := os.ReadFile(filepath.Join(filepath.Dir(kubectl), "args.txt"))
	s := string(args)
	if !strings.Contains(s, "--server-side") || !strings.Contains(s, "--force-conflicts") {
		t.Fatalf("SSA dry-run args missing: %s", s)
	}
	if !strings.Contains(s, "argocd-controller") {
		t.Fatalf("expected Argo field manager: %s", s)
	}
}

func TestHasLastAppliedConfiguration(t *testing.T) {
	if podtemplate.HasLastAppliedConfiguration(nil) {
		t.Fatal("nil")
	}
	if podtemplate.HasLastAppliedConfiguration(map[string]any{"metadata": map[string]any{}}) {
		t.Fatal("no annotations")
	}
	obj := map[string]any{
		"metadata": map[string]any{
			"annotations": map[string]any{
				"kubectl.kubernetes.io/last-applied-configuration": `{"kind":"Deployment"}`,
			},
		},
	}
	if !podtemplate.HasLastAppliedConfiguration(obj) {
		t.Fatal("expected last-applied present")
	}
}

func TestResolveDesiredDir_RecreateUsesClientSideNotFailClosed(t *testing.T) {
	// Live has RollingUpdate; desired switches to Recreate. Fake kubectl:
	// get returns last-applied + live template; apply (client) returns new template
	// with annotation (differs → Changed); must not use --server-side.
	script := `#!/bin/sh
args="$*"
echo "$args" >> "$(dirname "$0")/all-args.txt"
case "$1" in
  get)
    cat <<'EOF'
{"apiVersion":"apps/v1","kind":"Deployment","metadata":{"name":"api","namespace":"shop","annotations":{"kubectl.kubernetes.io/last-applied-configuration":"{}"}},"spec":{"strategy":{"type":"RollingUpdate","rollingUpdate":{"maxUnavailable":"100%","maxSurge":0}},"template":{"metadata":{"labels":{"app":"api"}},"spec":{"containers":[{"name":"api","image":"nginx"}]}}}}
EOF
    exit 0
    ;;
  apply)
    if echo "$args" | grep -q -- '--server-side'; then
      echo "SSA should not be used for Recreate test" >&2
      exit 1
    fi
    # Simulate successful client-side dry-run with Recreate (no rollingUpdate conflict)
    cat <<'EOF'
{"apiVersion":"apps/v1","kind":"Deployment","metadata":{"name":"api"},"spec":{"strategy":{"type":"Recreate"},"template":{"metadata":{"labels":{"app":"api"},"annotations":{"rehearsal.io/e2e":"recreate-switch"}},"spec":{"containers":[{"name":"api","image":"nginx"}]}}}}
EOF
    exit 0
    ;;
esac
echo "unexpected: $args" >&2
exit 1
`
	kubectl := writeFakeKubectl(t, script)
	dir := t.TempDir()
	manifest := `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: api
  namespace: shop
spec:
  strategy:
    type: Recreate
  template:
    metadata:
      labels:
        app: api
      annotations:
        rehearsal.io/e2e: recreate-switch
    spec:
      containers:
        - name: api
          image: nginx
`
	if err := os.WriteFile(filepath.Join(dir, "app.yaml"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	results, err := podtemplate.ResolveDesiredDir(context.Background(), dir, podtemplate.KubectlOptions{
		Kubectl: kubectl, Mode: podtemplate.ApplyClientSide,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("got %d results", len(results))
	}
	r := results[0]
	if r.Method == "fail-closed" {
		t.Fatalf("Recreate switch must succeed via client-side dry-run, got fail-closed: %s", r.Detail)
	}
	if r.Method != "client-side-dry-run" {
		t.Fatalf("method=%s detail=%s", r.Method, r.Detail)
	}
	if !r.Changed {
		t.Fatal("annotation change with Recreate must report Changed")
	}
	allArgs, _ := os.ReadFile(filepath.Join(filepath.Dir(kubectl), "all-args.txt"))
	if strings.Contains(string(allArgs), "--server-side") {
		t.Fatalf("must not use SSA: %s", allArgs)
	}
}

func TestResolveDesiredDir_EnvRemovalDetected(t *testing.T) {
	script := `#!/bin/sh
case "$1" in
  get)
    cat <<'EOF'
{"apiVersion":"apps/v1","kind":"Deployment","metadata":{"name":"api","namespace":"shop","annotations":{"kubectl.kubernetes.io/last-applied-configuration":"{\"spec\":{\"template\":{\"spec\":{\"containers\":[{\"name\":\"api\",\"env\":[{\"name\":\"FEATURE_FLAGS\"}]}]}}}}"}},"spec":{"template":{"metadata":{"labels":{"app":"api"}},"spec":{"containers":[{"name":"api","image":"nginx","env":[{"name":"FEATURE_FLAGS","value":"on"}]}]}}}}
EOF
    exit 0
    ;;
  apply)
    # Three-way client-side dry-run removes FEATURE_FLAGS
    cat <<'EOF'
{"apiVersion":"apps/v1","kind":"Deployment","metadata":{"name":"api"},"spec":{"template":{"metadata":{"labels":{"app":"api"}},"spec":{"containers":[{"name":"api","image":"nginx"}]}}}}
EOF
    exit 0
    ;;
esac
exit 1
`
	kubectl := writeFakeKubectl(t, script)
	dir := t.TempDir()
	manifest := `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: api
  namespace: shop
spec:
  template:
    metadata:
      labels:
        app: api
    spec:
      containers:
        - name: api
          image: nginx
`
	if err := os.WriteFile(filepath.Join(dir, "app.yaml"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	results, err := podtemplate.ResolveDesiredDir(context.Background(), dir, podtemplate.KubectlOptions{
		Kubectl: kubectl, Mode: podtemplate.ApplyClientSide,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || !results[0].Changed {
		t.Fatalf("env removal must be Changed: %+v", results)
	}
	if results[0].Method != "client-side-dry-run" {
		t.Fatalf("method=%s", results[0].Method)
	}
}

func TestResolveDesiredDir_NoLastAppliedFailClosed(t *testing.T) {
	script := `#!/bin/sh
case "$1" in
  get)
    cat <<'EOF'
{"apiVersion":"apps/v1","kind":"Deployment","metadata":{"name":"api","namespace":"shop"},"spec":{"template":{"metadata":{"labels":{"app":"api"}},"spec":{"containers":[{"name":"api","image":"nginx","env":[{"name":"FEATURE_FLAGS","value":"on"}]}]}}}}
EOF
    exit 0
    ;;
  apply|create)
    echo "must not call apply/create without last-applied" >&2
    exit 1
    ;;
esac
exit 1
`
	kubectl := writeFakeKubectl(t, script)
	dir := t.TempDir()
	manifest := `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: api
  namespace: shop
spec:
  template:
    metadata:
      labels:
        app: api
    spec:
      containers:
        - name: api
          image: nginx
`
	if err := os.WriteFile(filepath.Join(dir, "app.yaml"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	results, err := podtemplate.ResolveDesiredDir(context.Background(), dir, podtemplate.KubectlOptions{
		Kubectl: kubectl, Mode: podtemplate.ApplyClientSide,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("got %+v", results)
	}
	r := results[0]
	if !r.Changed || r.Method != "fail-closed" || !r.Uncertain {
		t.Fatalf("without last-applied must fail-closed uncertain: %+v", r)
	}
	if r.Detail != podtemplate.NoLastAppliedDetail {
		t.Fatalf("detail=%q", r.Detail)
	}
}

func TestHasArgoServerSideApply_AutoDetect(t *testing.T) {
	obj := map[string]any{
		"metadata": map[string]any{
			"managedFields": []any{
				map[string]any{"manager": "argocd-controller", "operation": "Apply"},
			},
		},
	}
	if !podtemplate.HasArgoServerSideApply(obj) {
		t.Fatal("expected SSA detect")
	}
	if got := podtemplate.ResolveApplyMode(obj, ""); got != podtemplate.ApplyServerSide {
		t.Fatalf("auto mode=%s", got)
	}
	if got := podtemplate.ResolveApplyMode(obj, podtemplate.ApplyClientSide); got != podtemplate.ApplyClientSide {
		t.Fatalf("force client override=%s", got)
	}
}

func TestResolveDesiredDir_IdenticalEqualsLive(t *testing.T) {
	script := `#!/bin/sh
tpl='{"metadata":{"labels":{"app":"api"}},"spec":{"containers":[{"name":"api","image":"nginx"}]}}'
case "$1" in
  get)
    printf '{"apiVersion":"apps/v1","kind":"Deployment","metadata":{"name":"api","namespace":"shop","annotations":{"kubectl.kubernetes.io/last-applied-configuration":"{}"}},"spec":{"template":%s}}\n' "$tpl"
    exit 0
    ;;
  apply)
    printf '{"apiVersion":"apps/v1","kind":"Deployment","metadata":{"name":"api"},"spec":{"template":%s}}\n' "$tpl"
    exit 0
    ;;
esac
exit 1
`
	kubectl := writeFakeKubectl(t, script)
	dir := t.TempDir()
	manifest := `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: api
  namespace: shop
spec:
  template:
    metadata:
      labels:
        app: api
    spec:
      containers:
        - name: api
          image: nginx
`
	if err := os.WriteFile(filepath.Join(dir, "app.yaml"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	results, err := podtemplate.ResolveDesiredDir(context.Background(), dir, podtemplate.KubectlOptions{
		Kubectl: kubectl, Mode: podtemplate.ApplyClientSide,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Changed {
		t.Fatalf("identical must be unchanged: %+v", results)
	}
}
