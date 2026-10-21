package podtemplate

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// KubectlOptions controls server-side dry-run apply via kubectl.
type KubectlOptions struct {
	Kubectl    string
	Kubeconfig string
	Context    string
	Timeout    time.Duration
	// FieldManager is passed to server-side apply (default "rehearsal").
	FieldManager string
}

// ServerSideDryRunTemplate applies obj with --server-side --dry-run=server and
// returns the resulting spec.template (with API defaults filled in).
func ServerSideDryRunTemplate(ctx context.Context, obj map[string]any, opts KubectlOptions) (map[string]any, error) {
	if obj == nil {
		return nil, fmt.Errorf("podtemplate: nil object for dry-run")
	}
	if opts.Kubectl == "" {
		opts.Kubectl = "kubectl"
	}
	if opts.FieldManager == "" {
		opts.FieldManager = "rehearsal"
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 60 * time.Second
	}
	if ctx == nil {
		ctx = context.Background()
	}
	cctx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()

	raw, err := yaml.Marshal(obj)
	if err != nil {
		return nil, err
	}
	args := []string{
		"apply",
		"--server-side",
		"--force-conflicts",
		"--field-manager", opts.FieldManager,
		"--dry-run=server",
		"-o", "json",
		"-f", "-",
	}
	if opts.Kubeconfig != "" {
		args = append([]string{"--kubeconfig", opts.Kubeconfig}, args...)
	}
	if opts.Context != "" {
		args = append([]string{"--context", opts.Context}, args...)
	}
	cmd := exec.CommandContext(cctx, opts.Kubectl, args...)
	cmd.Stdin = bytes.NewReader(raw)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > 400 {
			msg = msg[:400] + "…"
		}
		return nil, fmt.Errorf("kubectl server-side dry-run: %w: %s", err, msg)
	}
	var out map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		return nil, fmt.Errorf("decode dry-run result: %w", err)
	}
	spec, _ := out["spec"].(map[string]any)
	if spec == nil {
		return nil, fmt.Errorf("dry-run result missing spec")
	}
	tpl, _ := spec["template"].(map[string]any)
	if tpl == nil {
		return nil, fmt.Errorf("dry-run result missing spec.template")
	}
	return CloneMap(tpl), nil
}

// LiveGetTemplate fetches the live Deployment/StatefulSet/DaemonSet template.
func LiveGetTemplate(ctx context.Context, kind, ns, name string, opts KubectlOptions) (map[string]any, error) {
	if opts.Kubectl == "" {
		opts.Kubectl = "kubectl"
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 60 * time.Second
	}
	if ctx == nil {
		ctx = context.Background()
	}
	cctx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()
	resource := strings.ToLower(kind)
	switch resource {
	case "deployment", "deploy":
		resource = "deploy"
	case "statefulset", "sts":
		resource = "sts"
	case "daemonset", "ds":
		resource = "ds"
	}
	args := []string{"get", resource, name, "-n", ns, "-o", "json"}
	if opts.Kubeconfig != "" {
		args = append([]string{"--kubeconfig", opts.Kubeconfig}, args...)
	}
	if opts.Context != "" {
		args = append([]string{"--context", opts.Context}, args...)
	}
	cmd := exec.CommandContext(cctx, opts.Kubectl, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > 400 {
			msg = msg[:400] + "…"
		}
		return nil, fmt.Errorf("kubectl get %s/%s: %w: %s", resource, name, err, msg)
	}
	var out map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		return nil, err
	}
	spec, _ := out["spec"].(map[string]any)
	if spec == nil {
		return nil, fmt.Errorf("live object missing spec")
	}
	tpl, _ := spec["template"].(map[string]any)
	if tpl == nil {
		return nil, fmt.Errorf("live object missing spec.template")
	}
	return CloneMap(tpl), nil
}
