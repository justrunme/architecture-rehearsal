package podtemplate

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// WorkloadTemplateChange is the result of comparing one desired workload template
// against live (via apply dry-run when possible).
type WorkloadTemplateChange struct {
	ID      string // workload/<ns>/<name>
	Changed bool
	Method  string // "client-side-dry-run" | "server-side-dry-run" | "client-side-dry-run-two-way" | "subset" | "fail-closed"
	Detail  string
}

// ResolveDesiredDir compares each desired Deployment/StatefulSet/DaemonSet
// template to the live cluster using an apply dry-run that matches the sync tool.
//
// Default (ApplyClientSide): kubectl apply --dry-run=server (three-way merge via
// last-applied-configuration). Without that annotation, removals cannot be
// predicted — fail closed unless a two-way check proves equality.
//
// ApplyServerSide: SSA with Argo's field manager (argocd-controller) + force-conflicts.
// On dry-run failure the workload is marked Changed=true (fail-closed).
func ResolveDesiredDir(ctx context.Context, desiredDir string, opts KubectlOptions) ([]WorkloadTemplateChange, error) {
	opts.normalize()
	methodLabel := "client-side-dry-run"
	if opts.Mode == ApplyServerSide {
		methodLabel = "server-side-dry-run"
	}
	var out []WorkloadTemplateChange
	err := filepath.WalkDir(desiredDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if strings.HasPrefix(d.Name(), "..") {
				return filepath.SkipDir
			}
			return nil
		}
		name := strings.ToLower(d.Name())
		if !strings.HasSuffix(name, ".yaml") && !strings.HasSuffix(name, ".yml") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, doc := range strings.Split(string(raw), "\n---") {
			doc = strings.TrimSpace(doc)
			if doc == "" {
				continue
			}
			var obj map[string]any
			if err := yaml.Unmarshal([]byte(doc), &obj); err != nil || obj == nil {
				continue
			}
			kind, _ := obj["kind"].(string)
			if kind != "Deployment" && kind != "StatefulSet" && kind != "DaemonSet" {
				continue
			}
			meta, _ := obj["metadata"].(map[string]any)
			if meta == nil {
				continue
			}
			wname, _ := meta["name"].(string)
			ns, _ := meta["namespace"].(string)
			if ns == "" {
				ns = "default"
			}
			id := fmt.Sprintf("workload/%s/%s", ns, wname)
			tc := WorkloadTemplateChange{ID: id, Method: methodLabel}

			liveObj, err := LiveGetObject(ctx, kind, ns, wname, opts)
			if err != nil {
				tc.Changed = true
				tc.Method = "fail-closed"
				tc.Detail = err.Error()
				out = append(out, tc)
				continue
			}
			liveTpl, err := templateFromObject(liveObj)
			if err != nil {
				tc.Changed = true
				tc.Method = "fail-closed"
				tc.Detail = err.Error()
				out = append(out, tc)
				continue
			}

			if opts.Mode == ApplyClientSide && !HasLastAppliedConfiguration(liveObj) {
				resolveWithoutLastApplied(ctx, &tc, obj, liveTpl, opts)
				out = append(out, tc)
				continue
			}

			dryTpl, err := DryRunTemplate(ctx, obj, opts)
			if err != nil {
				tc.Changed = true
				tc.Method = "fail-closed"
				tc.Detail = err.Error()
				out = append(out, tc)
				continue
			}
			if !DeepEqual(sanitizeTemplateMeta(dryTpl), sanitizeTemplateMeta(liveTpl)) {
				tc.Changed = true
				tc.Detail = methodLabel + " template differs from live"
			} else {
				tc.Changed = false
				tc.Detail = methodLabel + " template equals live"
			}
			out = append(out, tc)
		}
		return nil
	})
	return out, err
}

// resolveWithoutLastApplied fails closed unless two-way equality holds:
// desired ⊆ dry-run result AND dry-run result ⊆ create-dry-run (desired + API defaults).
func resolveWithoutLastApplied(ctx context.Context, tc *WorkloadTemplateChange, obj map[string]any, liveTpl map[string]any, opts KubectlOptions) {
	desiredTpl, err := templateFromObject(obj)
	if err != nil {
		tc.Changed = true
		tc.Method = "fail-closed"
		tc.Detail = err.Error()
		return
	}
	dryTpl, err := DryRunTemplate(ctx, obj, opts)
	if err != nil {
		tc.Changed = true
		tc.Method = "fail-closed"
		tc.Detail = err.Error()
		return
	}
	defaultsTpl, err := CreateDryRunTemplate(ctx, obj, opts)
	if err != nil {
		tc.Changed = true
		tc.Method = "fail-closed"
		tc.Detail = "no last-applied-configuration; create dry-run failed: " + err.Error()
		return
	}
	d := sanitizeTemplateMeta(desiredTpl)
	dr := sanitizeTemplateMeta(dryTpl)
	def := sanitizeTemplateMeta(defaultsTpl)
	if SubsetChanged(d, dr) || SubsetChanged(dr, def) {
		tc.Changed = true
		tc.Method = "fail-closed"
		tc.Detail = "no last-applied-configuration; cannot predict field removals"
		return
	}
	tc.Method = "client-side-dry-run-two-way"
	if !DeepEqual(dr, sanitizeTemplateMeta(liveTpl)) {
		tc.Changed = true
		tc.Detail = "client-side dry-run (two-way) template differs from live"
	} else {
		tc.Changed = false
		tc.Detail = "client-side dry-run (two-way) template equals live"
	}
}

// CreateDryRunTemplate runs kubectl create --dry-run=server to get desired +
// API defaults without merging the live object (used for two-way equality).
func CreateDryRunTemplate(ctx context.Context, obj map[string]any, opts KubectlOptions) (map[string]any, error) {
	if obj == nil {
		return nil, fmt.Errorf("podtemplate: nil object for create dry-run")
	}
	opts.normalize()
	if ctx == nil {
		ctx = context.Background()
	}
	cctx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()
	raw, err := yaml.Marshal(obj)
	if err != nil {
		return nil, err
	}
	args := prependKubeArgs([]string{"create", "--dry-run=server", "-o", "json", "-f", "-"}, opts)
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
		return nil, fmt.Errorf("kubectl create dry-run: %w: %s", err, msg)
	}
	return templateFromJSON(stdout.Bytes())
}

func templateFromObject(obj map[string]any) (map[string]any, error) {
	if obj == nil {
		return nil, fmt.Errorf("nil object")
	}
	spec, _ := obj["spec"].(map[string]any)
	if spec == nil {
		return nil, fmt.Errorf("object missing spec")
	}
	tpl, _ := spec["template"].(map[string]any)
	if tpl == nil {
		return nil, fmt.Errorf("object missing spec.template")
	}
	return CloneMap(tpl), nil
}

// ParseApplyModeFromEnv reads REHEARSAL_SERVER_SIDE_APPLY / ARGOCD_SERVER_SIDE_APPLY.
func ParseApplyModeFromEnv() ApplyMode {
	for _, k := range []string{"REHEARSAL_SERVER_SIDE_APPLY", "ARGOCD_SERVER_SIDE_APPLY"} {
		v := strings.TrimSpace(strings.ToLower(os.Getenv(k)))
		switch v {
		case "1", "true", "yes", "on":
			return ApplyServerSide
		}
	}
	return ApplyClientSide
}
