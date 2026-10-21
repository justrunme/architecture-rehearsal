package podtemplate

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// WorkloadTemplateChange is the result of comparing one desired workload template
// against live (via apply dry-run when possible).
type WorkloadTemplateChange struct {
	ID      string // workload/<ns>/<name>
	Changed bool
	Method  string // "client-side-dry-run" | "server-side-dry-run" | "fail-closed" | …
	Detail  string
	// Uncertain is true when removals cannot be predicted (no last-applied in client mode).
	Uncertain bool
}

// ResolveDesiredDir compares each desired Deployment/StatefulSet/DaemonSet
// template to the live cluster using an apply dry-run that matches the sync tool.
//
// Default (auto): SSA when live managedFields show argocd-controller Apply;
// otherwise client-side three-way kubectl apply --dry-run=server.
// Without last-applied in client mode, fail closed with NoLastAppliedDetail
// (no create dry-run on an existing object).
func ResolveDesiredDir(ctx context.Context, desiredDir string, opts KubectlOptions) ([]WorkloadTemplateChange, error) {
	opts.normalize()
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
			tc := WorkloadTemplateChange{ID: id}

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

			mode := ResolveApplyMode(liveObj, opts.Mode)
			methodLabel := "client-side-dry-run"
			if mode == ApplyServerSide {
				methodLabel = "server-side-dry-run"
			}
			tc.Method = methodLabel
			runOpts := opts
			runOpts.Mode = mode

			if mode == ApplyClientSide && !HasLastAppliedConfiguration(liveObj) {
				tc.Changed = true
				tc.Method = "fail-closed"
				tc.Uncertain = true
				tc.Detail = NoLastAppliedDetail
				out = append(out, tc)
				continue
			}

			dryTpl, err := DryRunTemplate(ctx, obj, runOpts)
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
// When unset, returns empty (auto-detect per live object).
func ParseApplyModeFromEnv() ApplyMode {
	for _, k := range []string{"REHEARSAL_SERVER_SIDE_APPLY", "ARGOCD_SERVER_SIDE_APPLY"} {
		v := strings.TrimSpace(strings.ToLower(os.Getenv(k)))
		switch v {
		case "1", "true", "yes", "on":
			return ApplyServerSide
		case "0", "false", "no", "off", "client":
			return ApplyClientSide
		}
	}
	return "" // auto
}
