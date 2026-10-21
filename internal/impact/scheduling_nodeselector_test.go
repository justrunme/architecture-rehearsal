package impact_test

import (
	"strings"
	"testing"

	"github.com/justrunme/architecture-rehearsal/internal/graph"
	"github.com/justrunme/architecture-rehearsal/internal/impact"
	"github.com/justrunme/architecture-rehearsal/internal/loader"
)

func schedulingCtx(base *graph.Snapshot, ch *loader.ChangeEnvelope) impact.Context {
	prop := loader.ApplyChange(base, ch)
	return impact.Context{
		Baseline: base, Proposed: prop, Change: ch,
		BaseIdx: graph.BuildIndex(base), PropIdx: graph.BuildIndex(prop),
	}
}

func TestNodeSelector_MatchingLinuxPasses(t *testing.T) {
	base := &graph.Snapshot{
		ID: "ns-pass", Phase: graph.PhaseBaseline,
		Meta: map[string]any{"pod_scheduling_capacity_estimate": 50, "pdbsCollected": true},
		Nodes: []graph.Node{
			{ID: "cluster/c", Kind: graph.KindCluster, Name: "c"},
			{ID: "node/n1", Kind: graph.KindNode, Name: "n1", Attributes: map[string]any{
				"allocatablePods": 110,
				"labels": map[string]any{
					"kubernetes.io/os":            "linux",
					"topology.kubernetes.io/zone": "z1",
				},
				"zone": "z1",
			}},
			{ID: "workload/shop/api", Kind: graph.KindWorkload, Name: "api", Namespace: "shop",
				Attributes: map[string]any{"replicas": 3, "type": "Deployment", "podLabels": map[string]any{"app": "api"}}},
		},
	}
	ch := &loader.ChangeEnvelope{
		Kind: "argo-sync", ID: "ns-linux", Facts: map[string]any{"scenario": "argo-impact"},
		Seeds: []string{"workload/shop/api"},
		PatchNodes: []graph.Node{{
			ID: "workload/shop/api",
			Attributes: map[string]any{
				"replicas":     3,
				"nodeSelector": map[string]any{"kubernetes.io/os": "linux"},
			},
		}},
	}
	v := impact.SchedulingCapacity{}.Evaluate(schedulingCtx(base, ch))
	if v.Status != impact.Pass {
		t.Fatalf("matching kubernetes.io/os=linux want PASS, got %s: %s evidence=%v", v.Status, v.Explanation, v.Evidence)
	}
}

func TestNodeSelector_UnknownPoolBlocks(t *testing.T) {
	base := &graph.Snapshot{
		ID: "ns-block", Phase: graph.PhaseBaseline,
		Meta: map[string]any{"pod_scheduling_capacity_estimate": 50, "pdbsCollected": true},
		Nodes: []graph.Node{
			{ID: "cluster/c", Kind: graph.KindCluster, Name: "c"},
			{ID: "node/n1", Kind: graph.KindNode, Name: "n1", Attributes: map[string]any{
				"allocatablePods": 110,
				"labels": map[string]any{
					"kubernetes.io/os": "linux",
					"node-pool":        "general",
				},
			}},
			{ID: "workload/shop/api", Kind: graph.KindWorkload, Name: "api", Namespace: "shop",
				Attributes: map[string]any{"replicas": 3, "type": "Deployment"}},
		},
	}
	ch := &loader.ChangeEnvelope{
		Kind: "argo-sync", ID: "ns-highmem", Facts: map[string]any{"scenario": "argo-impact"},
		Seeds: []string{"workload/shop/api"},
		PatchNodes: []graph.Node{{
			ID: "workload/shop/api",
			Attributes: map[string]any{
				"replicas":     3,
				"nodeSelector": map[string]any{"node-pool": "high-mem"},
			},
		}},
	}
	v := impact.SchedulingCapacity{}.Evaluate(schedulingCtx(base, ch))
	if v.Status != impact.Block {
		t.Fatalf("node-pool=high-mem with no such node want BLOCK, got %s: %s", v.Status, v.Explanation)
	}
	joined := strings.Join(v.Evidence, " ")
	if !strings.Contains(joined, "matches zero nodes") {
		t.Fatalf("evidence should mention zero nodes, got %v", v.Evidence)
	}
}

func TestNodeSelector_MissingLabelSnapshotUnknown(t *testing.T) {
	base := &graph.Snapshot{
		ID: "ns-unk", Phase: graph.PhaseBaseline,
		Meta: map[string]any{"pod_scheduling_capacity_estimate": 50},
		Nodes: []graph.Node{
			{ID: "cluster/c", Kind: graph.KindCluster, Name: "c"},
			// Legacy snapshot: zone only, no labels map — must not false-BLOCK.
			{ID: "node/n1", Kind: graph.KindNode, Name: "n1", Attributes: map[string]any{
				"allocatablePods": 110,
				"zone":            "z1",
			}},
			{ID: "workload/shop/api", Kind: graph.KindWorkload, Name: "api", Namespace: "shop",
				Attributes: map[string]any{"replicas": 3, "type": "Deployment"}},
		},
	}
	ch := &loader.ChangeEnvelope{
		Kind: "argo-sync", ID: "ns-missing-labels", Facts: map[string]any{"scenario": "argo-impact"},
		Seeds: []string{"workload/shop/api"},
		PatchNodes: []graph.Node{{
			ID: "workload/shop/api",
			Attributes: map[string]any{
				"replicas":     3,
				"nodeSelector": map[string]any{"kubernetes.io/os": "linux"},
			},
		}},
	}
	v := impact.SchedulingCapacity{}.Evaluate(schedulingCtx(base, ch))
	if v.Status != impact.Unknown {
		t.Fatalf("missing node labels want UNKNOWN (not BLOCK), got %s: %s", v.Status, v.Explanation)
	}
	if !strings.Contains(v.Explanation, "label") {
		t.Fatalf("explanation should mention missing labels, got %q", v.Explanation)
	}
}
