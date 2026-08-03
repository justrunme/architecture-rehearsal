// Package change compiles rendered manifests / terraform plan JSON into ChangeEnvelopes.
package change

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/justrunme/architecture-rehearsal/internal/graph"
	"github.com/justrunme/architecture-rehearsal/internal/loader"
	"gopkg.in/yaml.v3"
)

// ManifestScope limits which baseline resources may be removed/compared.
// Without a scope, removals are DISABLED (safe default).
type ManifestScope struct {
	Namespaces  []string // only these namespaces
	NamePrefix  string   // workload name prefix
	LabelKey    string   // require baseline attr managed-by label key
	LabelValue  string
	AllowRemove bool // explicit opt-in to removals within scope
	// AllowPartial opts into continuing after YAML parse errors (default false = fail-closed).
	// When false, any malformed document fails the compile.
	AllowPartial bool
}

// FromManifestsDiff builds a change by comparing scoped baseline workloads to rendered YAML.
// Fail-closed: malformed YAML returns an error unless scope.AllowPartial is set.
func FromManifestsDiff(base *graph.Snapshot, manifestDir, changeID, title string, scope ManifestScope) (*loader.ChangeEnvelope, error) {
	baseWL := map[string]graph.Node{}
	for _, n := range base.Nodes {
		if n.Kind != graph.KindWorkload {
			continue
		}
		if !inScope(n, scope) {
			continue
		}
		key := normalizeNS(n.Namespace) + "/" + n.Name
		baseWL[key] = n
	}
	ch := &loader.ChangeEnvelope{
		APIVersion: graph.APIVersionV1Alpha1,
		Kind:       "k8s-manifest",
		ID:         changeID,
		Title:      title,
		Facts: map[string]any{
			"scope.namespaces":  scope.Namespaces,
			"scope.allowRemove": scope.AllowRemove,
			// Manifest/helm scale path is evaluated by capacity scenario.
			"scenario": "cni-ip-capacity",
		},
	}

	seen := map[string]bool{}
	parseErrors := 0
	var parseMsgs []string
	err := filepath.WalkDir(manifestDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
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
			if err := yaml.Unmarshal([]byte(doc), &obj); err != nil {
				parseErrors++
				parseMsgs = append(parseMsgs, fmt.Sprintf("%s: %v", path, err))
				if !scope.AllowPartial {
					return fmt.Errorf("yaml parse (fail-closed): %s: %w", path, err)
				}
				continue
			}
			if obj == nil {
				continue
			}
			// expand List
			if k, _ := obj["kind"].(string); k == "List" {
				items, _ := obj["items"].([]any)
				for _, it := range items {
					if m, ok := it.(map[string]any); ok {
						handleWorkloadObj(m, baseWL, seen, ch, scope)
					}
				}
				continue
			}
			handleWorkloadObj(obj, baseWL, seen, ch, scope)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if parseErrors > 0 {
		ch.Facts["yaml_parse_errors"] = parseErrors
		ch.Facts["coverage_gap"] = "yaml_parse_errors_present"
		if len(parseMsgs) > 0 {
			ch.Facts["yaml_parse_error_samples"] = parseMsgs
		}
	}

	// Removals only when explicitly allowed AND scope non-empty.
	if scope.AllowRemove && scopeActive(scope) {
		for key, bn := range baseWL {
			if !seen[key] {
				ch.RemovedNodes = append(ch.RemovedNodes, bn.ID)
				ch.Seeds = append(ch.Seeds, bn.ID)
			}
		}
	} else if !scope.AllowRemove {
		ch.Facts["removals"] = "disabled_without_allowRemove"
	}

	return ch, nil
}

func scopeActive(s ManifestScope) bool {
	return len(s.Namespaces) > 0 || s.NamePrefix != "" || s.LabelKey != ""
}

func inScope(n graph.Node, s ManifestScope) bool {
	if !scopeActive(s) {
		// no scope: include all for patch/add, but removals disabled separately
		return true
	}
	ns := normalizeNS(n.Namespace)
	if len(s.Namespaces) > 0 {
		ok := false
		for _, x := range s.Namespaces {
			if normalizeNS(x) == ns {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	if s.NamePrefix != "" && !strings.HasPrefix(n.Name, s.NamePrefix) {
		return false
	}
	if s.LabelKey != "" {
		// check attributes labels
		if ml, ok := n.Attributes["podLabels"].(map[string]any); ok {
			if fmt.Sprint(ml[s.LabelKey]) != s.LabelValue {
				return false
			}
		} else if n.AttrString(s.LabelKey) != s.LabelValue {
			return false
		}
	}
	return true
}

func handleWorkloadObj(obj map[string]any, baseWL map[string]graph.Node, seen map[string]bool, ch *loader.ChangeEnvelope, scope ManifestScope) {
	kind, _ := obj["kind"].(string)
	if kind != "Deployment" && kind != "StatefulSet" && kind != "DaemonSet" {
		return
	}
	meta, _ := obj["metadata"].(map[string]any)
	if meta == nil {
		return
	}
	wname, _ := meta["name"].(string)
	ns, _ := meta["namespace"].(string)
	ns = normalizeNS(ns)
	// filter by scope namespaces if set
	tmp := graph.Node{Name: wname, Namespace: ns, Kind: graph.KindWorkload, Attributes: map[string]any{}}
	if labels, ok := meta["labels"].(map[string]any); ok {
		tmp.Attributes["podLabels"] = labels
	}
	if !inScope(tmp, scope) && scopeActive(scope) {
		return
	}
	key := ns + "/" + wname
	seen[key] = true
	replicas := 1
	maxSurge := ""
	if spec, ok := obj["spec"].(map[string]any); ok {
		if v, ok := spec["replicas"]; ok {
			switch t := v.(type) {
			case int:
				replicas = t
			case float64:
				replicas = int(t)
			}
		}
		if strat, ok := spec["strategy"].(map[string]any); ok {
			if ru, ok := strat["rollingUpdate"].(map[string]any); ok {
				if ms, ok := ru["maxSurge"]; ok {
					maxSurge = fmt.Sprint(ms)
				}
			}
		}
	}
	id := fmt.Sprintf("workload/%s/%s", ns, wname)
	attrs := map[string]any{"replicas": replicas, "type": kind}
	if maxSurge != "" {
		attrs["maxSurge"] = maxSurge
	}
	if bn, ok := baseWL[key]; ok {
		if bn.WorkloadReplicas() != replicas || (maxSurge != "" && bn.AttrString("maxSurge") != maxSurge) {
			ch.PatchNodes = append(ch.PatchNodes, graph.Node{ID: id, Attributes: attrs})
			ch.Seeds = append(ch.Seeds, id)
		}
	} else {
		ch.AddedNodes = append(ch.AddedNodes, graph.Node{
			ID: id, Kind: graph.KindWorkload, Name: wname, Namespace: ns, Attributes: attrs,
		})
		ch.Seeds = append(ch.Seeds, id)
	}
}

func normalizeNS(ns string) string {
	if ns == "" {
		return "default"
	}
	return ns
}

// FromTerraformPlan extracts change facts and valid graph seeds from terraform show -json.
func FromTerraformPlan(path, changeID, title string, base *graph.Snapshot) (*loader.ChangeEnvelope, error) {
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
		Facts:      map[string]any{},
	}
	// Prefer cluster/node seeds from baseline when TF touches node groups.
	var nodeIDs []string
	if base != nil {
		for _, n := range base.Nodes {
			if n.Kind == graph.KindNode {
				nodeIDs = append(nodeIDs, n.ID)
			}
		}
		if len(nodeIDs) > 0 {
			// capacity scenario seed: first node / cluster
			ch.Seeds = append(ch.Seeds, nodeIDs[0])
			if c := base.Labels["cluster"]; c != "" {
				ch.Seeds = append(ch.Seeds, "cluster/"+c)
			}
		}
	}
	rcs, _ := plan["resource_changes"].([]any)
	unsupported := 0
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
		if strings.Contains(typ, "node_group") || strings.Contains(typ, "autoscaling_group") {
			ch.Facts["scenario"] = "cni-ip-capacity"
			ch.Kind = "terraform-plan"
			after, _ := change["after"].(map[string]any)
			before, _ := change["before"].(map[string]any)
			if after != nil {
				if ds, ok := after["desired_size"]; ok {
					ch.Facts["terraform_desired_size"] = ds
				}
			}
			if before != nil && after != nil {
				ch.Facts["terraform_nodegroup_change"] = true
			}
			// do NOT add tf: address as seed
			_ = addr
		} else if action == "delete" && (strings.Contains(typ, "instance") || strings.Contains(strings.ToLower(addr), "node")) {
			ch.Kind = "node-failure"
			ch.Facts["event"] = "node_loss"
			ch.Facts["scenario"] = "rwo-node-loss"
			// if only one node in baseline, mark it removed
			if len(nodeIDs) == 1 {
				ch.RemovedNodes = []string{nodeIDs[0]}
				ch.Facts["lost_node"] = nodeIDs[0]
				ch.Seeds = []string{nodeIDs[0]}
			} else if len(nodeIDs) > 0 {
				// cannot know which — coverage gap
				ch.Facts["coverage_gap"] = "node_delete_ambiguous"
				ch.Facts["lost_node_candidates"] = len(nodeIDs)
			}
		} else {
			unsupported++
		}
	}
	if unsupported > 0 {
		ch.Facts["unsupported_resource_changes"] = unsupported
	}
	if len(ch.Seeds) == 0 && base != nil && len(base.Nodes) > 0 {
		// last resort: seed cluster node if any
		for _, n := range base.Nodes {
			if n.Kind == graph.KindCluster {
				ch.Seeds = []string{n.ID}
				break
			}
		}
	}
	return ch, nil
}
