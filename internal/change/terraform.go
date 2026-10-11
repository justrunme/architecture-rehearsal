package change

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/justrunme/architecture-rehearsal/internal/graph"
	"github.com/justrunme/architecture-rehearsal/internal/loader"
)

// DefaultUnsupportedThreshold marks the change inconclusive when unsupported
// resource changes meet or exceed this count.
const DefaultUnsupportedThreshold = 1

// tfPlan is the structural subset of `terraform show -json` we consume.
type tfPlan struct {
	ResourceChanges []tfResourceChange `json:"resource_changes"`
}

type tfResourceChange struct {
	Address string    `json:"address"`
	Type    string    `json:"type"`
	Name    string    `json:"name"`
	Change  *tfChange `json:"change"`
}

type tfChange struct {
	Actions []string       `json:"actions"`
	Before  map[string]any `json:"before"`
	After   map[string]any `json:"after"`
}

// tfCategory classifies how a Terraform resource type maps into rehearsal.
type tfCategory string

const (
	tfCatNodeGroup tfCategory = "node_group"
	tfCatInstance  tfCategory = "instance"
	tfCatVolume    tfCategory = "volume"
	tfCatNetwork   tfCategory = "network"
)

// tfResourceRule is a data-driven classifier entry (exact type match).
type tfResourceRule struct {
	Type     string
	Category tfCategory
	Scenario string
	Kind     string
}

// terraformClassifier maps known Terraform resource types to rehearsal semantics.
var terraformClassifier = []tfResourceRule{
	{Type: "aws_eks_node_group", Category: tfCatNodeGroup, Scenario: "cni-ip-capacity", Kind: "terraform-plan"},
	{Type: "aws_autoscaling_group", Category: tfCatNodeGroup, Scenario: "cni-ip-capacity", Kind: "terraform-plan"},
	{Type: "google_container_node_pool", Category: tfCatNodeGroup, Scenario: "cni-ip-capacity", Kind: "terraform-plan"},
	{Type: "azurerm_kubernetes_cluster_node_pool", Category: tfCatNodeGroup, Scenario: "cni-ip-capacity", Kind: "terraform-plan"},
	{Type: "aws_instance", Category: tfCatInstance, Scenario: "rwo-node-loss", Kind: "node-failure"},
	{Type: "google_compute_instance", Category: tfCatInstance, Scenario: "rwo-node-loss", Kind: "node-failure"},
	{Type: "azurerm_linux_virtual_machine", Category: tfCatInstance, Scenario: "rwo-node-loss", Kind: "node-failure"},
	{Type: "azurerm_windows_virtual_machine", Category: tfCatInstance, Scenario: "rwo-node-loss", Kind: "node-failure"},
	{Type: "aws_ebs_volume", Category: tfCatVolume, Scenario: "", Kind: "terraform-plan"},
	{Type: "aws_security_group_rule", Category: tfCatNetwork, Scenario: "", Kind: "terraform-plan"},
	{Type: "google_compute_firewall", Category: tfCatNetwork, Scenario: "", Kind: "terraform-plan"},
}

func lookupTFRule(typ string) (tfResourceRule, bool) {
	for _, r := range terraformClassifier {
		if r.Type == typ {
			return r, true
		}
	}
	return tfResourceRule{}, false
}

// FromTerraformPlan extracts change facts and valid graph seeds from terraform show -json.
// Classification is structural (typed JSON + exact resource type table), not substring heuristics.
func FromTerraformPlan(path, changeID, title string, base *graph.Snapshot) (*loader.ChangeEnvelope, error) {
	return FromTerraformPlanWithThreshold(path, changeID, title, base, DefaultUnsupportedThreshold)
}

// FromTerraformPlanWithThreshold is FromTerraformPlan with an explicit unsupported threshold.
func FromTerraformPlanWithThreshold(path, changeID, title string, base *graph.Snapshot, unsupportedThreshold int) (*loader.ChangeEnvelope, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var plan tfPlan
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

	var nodeIDs []string
	if base != nil {
		for _, n := range base.Nodes {
			if n.Kind == graph.KindNode {
				nodeIDs = append(nodeIDs, n.ID)
			}
		}
		if len(nodeIDs) > 0 {
			ch.Seeds = append(ch.Seeds, nodeIDs[0])
			if c := base.Labels["cluster"]; c != "" {
				ch.Seeds = append(ch.Seeds, "cluster/"+c)
			}
		}
	}

	unsupported := 0
	var unsupportedAddrs []string
	for _, rc := range plan.ResourceChanges {
		if rc.Change == nil || len(rc.Change.Actions) == 0 {
			continue
		}
		action := rc.Change.Actions[0]
		rule, ok := lookupTFRule(rc.Type)
		if !ok {
			unsupported++
			unsupportedAddrs = append(unsupportedAddrs, rc.Address)
			continue
		}
		switch rule.Category {
		case tfCatNodeGroup:
			applyNodeGroupChange(ch, rule, rc)
		case tfCatInstance:
			if action == "delete" || (len(rc.Change.Actions) == 2 && rc.Change.Actions[0] == "delete") {
				applyInstanceDelete(ch, rule, rc, base, nodeIDs)
			} else {
				// Non-delete instance changes are tracked but do not alter node-loss seeds.
				ch.Facts["terraform_instance_change"] = true
				if rule.Scenario != "" && ch.Facts["scenario"] == nil {
					ch.Facts["scenario"] = rule.Scenario
				}
			}
		case tfCatVolume, tfCatNetwork:
			ch.Facts["terraform_"+string(rule.Category)+"_change"] = true
			tracked, _ := ch.Facts["supported_resource_changes"].(int)
			ch.Facts["supported_resource_changes"] = tracked + 1
		default:
			unsupported++
			unsupportedAddrs = append(unsupportedAddrs, rc.Address)
		}
	}

	if unsupported > 0 {
		ch.Facts["unsupported_resource_changes"] = unsupported
		ch.Facts["unsupported_resource_addresses"] = unsupportedAddrs
	}
	if unsupportedThreshold > 0 && unsupported >= unsupportedThreshold {
		ch.Facts["coverage_gap"] = "unsupported_resource_changes"
	}

	if len(ch.Seeds) == 0 && base != nil && len(base.Nodes) > 0 {
		for _, n := range base.Nodes {
			if n.Kind == graph.KindCluster {
				ch.Seeds = []string{n.ID}
				break
			}
		}
	}
	return ch, nil
}

func applyNodeGroupChange(ch *loader.ChangeEnvelope, rule tfResourceRule, rc tfResourceChange) {
	ch.Kind = rule.Kind
	if rule.Scenario != "" {
		ch.Facts["scenario"] = rule.Scenario
	}
	ch.Facts["terraform_nodegroup_change"] = true
	ch.Facts["terraform_resource_type"] = rc.Type
	ch.Facts["terraform_resource_address"] = rc.Address
	if rc.Change != nil && rc.Change.After != nil {
		after := rc.Change.After
		for _, key := range []string{"desired_size", "desired_capacity", "node_count"} {
			if ds, ok := after[key]; ok {
				ch.Facts["terraform_desired_size"] = ds
				break
			}
		}
		if mx, ok := after["max_size"]; ok {
			ch.Facts["terraform_max_size"] = mx
		}
	}
}

func applyInstanceDelete(ch *loader.ChangeEnvelope, rule tfResourceRule, rc tfResourceChange, base *graph.Snapshot, nodeIDs []string) {
	ch.Kind = rule.Kind
	ch.Facts["event"] = "node_loss"
	if rule.Scenario != "" {
		ch.Facts["scenario"] = rule.Scenario
	}
	ch.Facts["terraform_resource_type"] = rc.Type
	ch.Facts["terraform_resource_address"] = rc.Address

	if lost := resolveTargetNode(base, rc); lost != "" {
		ch.RemovedNodes = []string{lost}
		ch.Facts["lost_node"] = lost
		ch.Seeds = []string{lost}
		return
	}
	if len(nodeIDs) == 1 {
		ch.RemovedNodes = []string{nodeIDs[0]}
		ch.Facts["lost_node"] = nodeIDs[0]
		ch.Seeds = []string{nodeIDs[0]}
		return
	}
	if len(nodeIDs) > 0 {
		ch.Facts["coverage_gap"] = "node_delete_ambiguous"
		ch.Facts["lost_node_candidates"] = len(nodeIDs)
	}
}

// resolveTargetNode matches a deleted Terraform instance to a baseline Node via
// provider IDs, private DNS / name attributes, or address tokens.
func resolveTargetNode(base *graph.Snapshot, rc tfResourceChange) string {
	if base == nil {
		return ""
	}
	candidates := collectInstanceIdentity(rc)
	if len(candidates) == 0 {
		return ""
	}
	for _, n := range base.Nodes {
		if n.Kind != graph.KindNode {
			continue
		}
		attrs := []string{
			n.Name,
			n.ID,
			n.AttrString("providerID"),
			n.AttrString("providerId"),
			n.AttrString("instanceId"),
			n.AttrString("instanceID"),
			n.AttrString("privateDNS"),
			n.AttrString("privateDns"),
			n.AttrString("private_dns"),
		}
		for _, c := range candidates {
			cl := strings.ToLower(c)
			for _, a := range attrs {
				if a == "" {
					continue
				}
				al := strings.ToLower(a)
				if al == cl || strings.Contains(al, cl) || strings.Contains(cl, al) {
					return n.ID
				}
			}
		}
	}
	return ""
}

func collectInstanceIdentity(rc tfResourceChange) []string {
	seen := map[string]bool{}
	var out []string
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			return
		}
		seen[s] = true
		out = append(out, s)
	}
	add(rc.Address)
	add(rc.Name)
	for _, m := range []map[string]any{nilOrMap(rc.Change, true), nilOrMap(rc.Change, false)} {
		if m == nil {
			continue
		}
		for _, k := range []string{"id", "instance_id", "private_dns", "private_ip", "name", "hostname"} {
			if v, ok := m[k]; ok {
				add(fmt.Sprint(v))
			}
		}
		if tags, ok := m["tags"].(map[string]any); ok {
			if v, ok := tags["Name"]; ok {
				add(fmt.Sprint(v))
			}
		}
	}
	return out
}

func nilOrMap(c *tfChange, before bool) map[string]any {
	if c == nil {
		return nil
	}
	if before {
		return c.Before
	}
	return c.After
}
