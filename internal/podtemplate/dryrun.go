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

// ApplyMode selects how the live template dry-run mimics the sync tool.
type ApplyMode string

const (
	// ApplyAuto picks SSA when live managedFields show argocd-controller Apply,
	// otherwise client-side three-way apply. Used when Mode is empty.
	ApplyAuto ApplyMode = "auto"
	// ApplyClientSide is Argo CD's default sync: client-side three-way merge
	// using kubectl.kubernetes.io/last-applied-configuration.
	ApplyClientSide ApplyMode = "client"
	// ApplyServerSide is Argo CD ServerSideApply=true: SSA with Argo's manager.
	ApplyServerSide ApplyMode = "ssa"
)

// DefaultArgoFieldManager is Argo CD's server-side apply field manager.
const DefaultArgoFieldManager = "argocd-controller"

const lastAppliedAnnotation = "kubectl.kubernetes.io/last-applied-configuration"

// NoLastAppliedDetail is the fail-closed reason when three-way merge is impossible.
const NoLastAppliedDetail = "cannot predict removals: live object has no last-applied-configuration (created with kubectl create or SSA); treating as rollout"

// KubectlOptions controls dry-run apply via kubectl.
type KubectlOptions struct {
	Kubectl    string
	Kubeconfig string
	Context    string
	Timeout    time.Duration
	// Mode: empty/ApplyAuto → per-object auto-detect; ApplyClientSide / ApplyServerSide force.
	Mode ApplyMode
	// FieldManager is used only for ApplyServerSide (default argocd-controller).
	FieldManager string
}

func (o *KubectlOptions) normalize() {
	if o.Kubectl == "" {
		o.Kubectl = "kubectl"
	}
	if o.Timeout <= 0 {
		o.Timeout = 60 * time.Second
	}
	if o.FieldManager == "" {
		o.FieldManager = DefaultArgoFieldManager
	}
}

// ResolveApplyMode returns the effective apply mode for a live object.
// Forced Mode (client/ssa) wins; otherwise SSA when managedFields show
// argocd-controller with operation Apply.
func ResolveApplyMode(liveObj map[string]any, mode ApplyMode) ApplyMode {
	switch mode {
	case ApplyClientSide, ApplyServerSide:
		return mode
	}
	if HasArgoServerSideApply(liveObj) {
		return ApplyServerSide
	}
	return ApplyClientSide
}

// HasArgoServerSideApply reports whether managedFields include Argo's SSA manager.
func HasArgoServerSideApply(obj map[string]any) bool {
	if obj == nil {
		return false
	}
	meta, _ := obj["metadata"].(map[string]any)
	if meta == nil {
		return false
	}
	mfs, ok := meta["managedFields"].([]any)
	if !ok {
		return false
	}
	for _, raw := range mfs {
		mf, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		mgr := fmt.Sprint(mf["manager"])
		op := fmt.Sprint(mf["operation"])
		if mgr == DefaultArgoFieldManager && op == "Apply" {
			return true
		}
	}
	return false
}

// DryRunTemplate applies obj the same way the sync will, with --dry-run=server,
// and returns the resulting spec.template.
func DryRunTemplate(ctx context.Context, obj map[string]any, opts KubectlOptions) (map[string]any, error) {
	if obj == nil {
		return nil, fmt.Errorf("podtemplate: nil object for dry-run")
	}
	opts.normalize()
	if opts.Mode == "" || opts.Mode == ApplyAuto {
		opts.Mode = ApplyClientSide
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
	var args []string
	switch opts.Mode {
	case ApplyServerSide:
		args = []string{
			"apply",
			"--server-side",
			"--force-conflicts",
			"--field-manager", opts.FieldManager,
			"--dry-run=server",
			"-o", "json",
			"-f", "-",
		}
	case ApplyClientSide:
		args = []string{"apply", "--dry-run=server", "-o", "json", "-f", "-"}
	default:
		return nil, fmt.Errorf("podtemplate: unknown apply mode %q", opts.Mode)
	}
	args = prependKubeArgs(args, opts)
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
		return nil, fmt.Errorf("kubectl %s dry-run: %w: %s", opts.Mode, err, msg)
	}
	return templateFromJSON(stdout.Bytes())
}

// ServerSideDryRunTemplate is kept for callers; it forces ApplyServerSide.
func ServerSideDryRunTemplate(ctx context.Context, obj map[string]any, opts KubectlOptions) (map[string]any, error) {
	opts.Mode = ApplyServerSide
	return DryRunTemplate(ctx, obj, opts)
}

// LiveGetObject returns the full live object JSON as a map.
func LiveGetObject(ctx context.Context, kind, ns, name string, opts KubectlOptions) (map[string]any, error) {
	opts.normalize()
	if ctx == nil {
		ctx = context.Background()
	}
	cctx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()
	resource := resourceName(kind)
	// --show-managed-fields is required for SSA auto-detect (kubectl omits
	// managedFields from -o json unless this flag is set).
	args := prependKubeArgs([]string{"get", resource, name, "-n", ns, "-o", "json", "--show-managed-fields"}, opts)
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
	return out, nil
}

// LiveGetTemplate fetches the live workload pod template.
func LiveGetTemplate(ctx context.Context, kind, ns, name string, opts KubectlOptions) (map[string]any, error) {
	obj, err := LiveGetObject(ctx, kind, ns, name, opts)
	if err != nil {
		return nil, err
	}
	spec, _ := obj["spec"].(map[string]any)
	if spec == nil {
		return nil, fmt.Errorf("live object missing spec")
	}
	tpl, _ := spec["template"].(map[string]any)
	if tpl == nil {
		return nil, fmt.Errorf("live object missing spec.template")
	}
	return CloneMap(tpl), nil
}

// HasLastAppliedConfiguration reports whether the live object has the
// kubectl last-applied-configuration annotation required for three-way merge.
func HasLastAppliedConfiguration(obj map[string]any) bool {
	if obj == nil {
		return false
	}
	meta, _ := obj["metadata"].(map[string]any)
	if meta == nil {
		return false
	}
	ann, _ := meta["annotations"].(map[string]any)
	if ann == nil {
		return false
	}
	v, ok := ann[lastAppliedAnnotation]
	if !ok {
		return false
	}
	s := strings.TrimSpace(fmt.Sprint(v))
	return s != "" && s != "<nil>"
}

func templateFromJSON(raw []byte) (map[string]any, error) {
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
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

func resourceName(kind string) string {
	switch strings.ToLower(kind) {
	case "deployment", "deploy":
		return "deploy"
	case "statefulset", "sts":
		return "sts"
	case "daemonset", "ds":
		return "ds"
	default:
		return strings.ToLower(kind)
	}
}

func prependKubeArgs(args []string, opts KubectlOptions) []string {
	out := args
	if opts.Kubeconfig != "" {
		out = append([]string{"--kubeconfig", opts.Kubeconfig}, out...)
	}
	if opts.Context != "" {
		out = append([]string{"--context", opts.Context}, out...)
	}
	return out
}
