// Package change compiles rendered manifests / terraform plan JSON into ChangeEnvelopes.
package change

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/justrunme/architecture-rehearsal/internal/graph"
	"github.com/justrunme/architecture-rehearsal/internal/loader"
	"gopkg.in/yaml.v3"
)

// FromManifestsDiff builds a change by comparing baseline workloads to a rendered YAML dir.
func FromManifestsDiff(base *graph.Snapshot, manifestDir, changeID, title string) (*loader.ChangeEnvelope, error) {
	// Index baseline workloads by ns/name
	baseWL := map[string]graph.Node{}
	for _, n := range base.Nodes {
		if n.Kind == graph.KindWorkload {
			baseWL[n.Namespace+"/"+n.Name] = n
		}
	}
	ch := &loader.ChangeEnvelope{
		APIVersion: graph.APIVersionV1Alpha1,
		Kind:       "k8s-manifest",
		ID:         changeID,
		Title:      title,
		Facts:      map[string]any{},
	}
	entries, err := os.ReadDir(manifestDir)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := strings.ToLower(e.Name())
		if !strings.HasSuffix(name, ".yaml") && !strings.HasSuffix(name, ".yml") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(manifestDir, e.Name()))
		if err != nil {
			return nil, err
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
			key := ns + "/" + wname
			seen[key] = true
			replicas := 1
			if spec, ok := obj["spec"].(map[string]any); ok {
				switch v := spec["replicas"].(type) {
				case int:
					replicas = v
				case float64:
					replicas = int(v)
				}
			}
			id := fmt.Sprintf("workload/%s/%s", ns, wname)
			if bn, ok := baseWL[key]; ok {
				if bn.WorkloadReplicas() != replicas {
					ch.PatchNodes = append(ch.PatchNodes, graph.Node{
						ID: id, Attributes: map[string]any{"replicas": replicas},
					})
					ch.Seeds = append(ch.Seeds, id)
				}
			} else {
				ch.AddedNodes = append(ch.AddedNodes, graph.Node{
					ID: id, Kind: graph.KindWorkload, Name: wname, Namespace: ns,
					Attributes: map[string]any{"type": kind, "replicas": replicas},
				})
				ch.Seeds = append(ch.Seeds, id)
			}
		}
	}
	// removals: baseline workloads not in rendered set (only if dir non-empty)
	if len(seen) > 0 {
		for key, bn := range baseWL {
			if !seen[key] {
				ch.RemovedNodes = append(ch.RemovedNodes, bn.ID)
				ch.Seeds = append(ch.Seeds, bn.ID)
			}
		}
	}
	return ch, nil
}

// FromTerraformPlan extracts a minimal change from terraform show -json output.
// Looks for aws_eks_node_group / kubernetes_node_group scale and null_resource markers.
func FromTerraformPlan(path, changeID, title string) (*loader.ChangeEnvelope, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var plan map[string]any
	if err := json.Unmarshal(raw, &plan); err != nil {
		return nil, err
	}
	ch := &loader.ChangeEnvelope{
		APIVersion: graph.APIVersionV1Alpha1,
		Kind:       "terraform-plan",
		ID:         changeID,
		Title:      title,
		Facts:      map[string]any{"scenario": "cni-ip-capacity"},
	}
	// resource_changes
	rcs, _ := plan["resource_changes"].([]any)
	for _, rc := range rcs {
		m, ok := rc.(map[string]any)
		if !ok {
			continue
		}
		typ, _ := m["type"].(string)
		addr, _ := m["address"].(string)
		change, _ := m["change"].(map[string]any)
		if change == nil {
			continue
		}
		actions, _ := change["actions"].([]any)
		action := ""
		if len(actions) > 0 {
			action, _ = actions[0].(string)
		}
		// Node group scaling → capacity fact
		if strings.Contains(typ, "node_group") || strings.Contains(typ, "autoscaling_group") {
			after, _ := change["after"].(map[string]any)
			before, _ := change["before"].(map[string]any)
			if after != nil {
				if ds, ok := after["desired_size"]; ok {
					ch.Facts["terraform_desired_size"] = ds
				}
				if ds, ok := after["desired_capacity"]; ok {
					ch.Facts["terraform_desired_size"] = ds
				}
			}
			if before != nil && after != nil {
				ch.Facts["terraform_nodegroup_change"] = true
			}
			ch.Seeds = append(ch.Seeds, "tf:"+addr)
		}
		if action == "delete" && strings.Contains(strings.ToLower(addr), "node") {
			ch.Kind = "node-failure"
			ch.Facts["event"] = "node_loss"
			ch.Facts["scenario"] = "rwo-node-loss"
		}
	}
	return ch, nil
}
