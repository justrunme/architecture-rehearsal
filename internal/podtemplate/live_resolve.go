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
// against live (via server-side dry-run when possible).
type WorkloadTemplateChange struct {
	ID      string // workload/<ns>/<name>
	Changed bool
	Method  string // "server-side-dry-run" | "subset" | "fail-closed"
	Detail  string
}

// ResolveDesiredDir compares each desired Deployment/StatefulSet/DaemonSet
// template to the live cluster using server-side dry-run apply.
// On dry-run failure the workload is marked Changed=true (fail-closed).
func ResolveDesiredDir(ctx context.Context, desiredDir string, opts KubectlOptions) ([]WorkloadTemplateChange, error) {
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
			tc := WorkloadTemplateChange{ID: id, Method: "server-side-dry-run"}
			dryTpl, err := ServerSideDryRunTemplate(ctx, obj, opts)
			if err != nil {
				tc.Changed = true
				tc.Method = "fail-closed"
				tc.Detail = err.Error()
				out = append(out, tc)
				continue
			}
			liveTpl, err := LiveGetTemplate(ctx, kind, ns, wname, opts)
			if err != nil {
				// Object may not exist yet → creating it rolls out new pods.
				tc.Changed = true
				tc.Method = "fail-closed"
				tc.Detail = err.Error()
				out = append(out, tc)
				continue
			}
			if !DeepEqual(sanitizeTemplateMeta(dryTpl), sanitizeTemplateMeta(liveTpl)) {
				tc.Changed = true
				tc.Detail = "server-side dry-run template differs from live"
			} else {
				tc.Changed = false
				tc.Detail = "server-side dry-run template equals live"
			}
			out = append(out, tc)
		}
		return nil
	})
	return out, err
}
