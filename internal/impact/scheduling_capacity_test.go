package impact_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/justrunme/architecture-rehearsal/internal/graph"
	"github.com/justrunme/architecture-rehearsal/internal/impact"
	"github.com/justrunme/architecture-rehearsal/internal/loader"
)

func TestScheduling_CrossNamespacePodsBlockScale(t *testing.T) {
	// Node allocatable 110; 106 non-terminal pods from other namespaces already occupy slots.
	// Scaling shop/api 3→6 needs delta+surge > remaining → BLOCK even when inventory is "namespaced".
	base := &graph.Snapshot{
		ID: "xns-cap", Phase: graph.PhaseBaseline,
		Meta: map[string]any{
			"pods_collect_ok":  true,
			"nodes_collect_ok": true,
			"pdbsCollected":    true,
		},
		Nodes: []graph.Node{
			{ID: "cluster/c", Kind: graph.KindCluster, Name: "c"},
			{ID: "node/n1", Kind: graph.KindNode, Name: "n1", Attributes: map[string]any{
				"allocatablePods": 110,
				"labels":          map[string]any{"kubernetes.io/os": "linux"},
			}},
			{ID: "workload/shop/api", Kind: graph.KindWorkload, Name: "api", Namespace: "shop",
				Attributes: map[string]any{"replicas": 3, "type": "Deployment", "maxSurge": "25%"}},
		},
	}
	for i := 0; i < 106; i++ {
		base.Nodes = append(base.Nodes, graph.Node{
			ID: fmt.Sprintf("pod/other/filler-%d", i),
			Kind: graph.KindPod, Name: fmt.Sprintf("filler-%d", i), Namespace: "other",
			Attributes: map[string]any{"phase": "Running", "nodeName": "n1"},
		})
	}
	ch := &loader.ChangeEnvelope{
		Kind: "argo-sync", ID: "scale", Facts: map[string]any{"scenario": "argo-impact"},
		Seeds: []string{"workload/shop/api"},
		PatchNodes: []graph.Node{{
			ID: "workload/shop/api",
			Attributes: map[string]any{
				"replicas": 6, "maxSurge": "25%",
			},
		}},
	}
	v := impact.SchedulingCapacity{}.Evaluate(schedulingCtx(base, ch))
	if v.Status != impact.Block {
		t.Fatalf("cross-ns full node + scale want BLOCK, got %s: %s evidence=%v", v.Status, v.Explanation, v.Evidence)
	}
}

func TestScheduling_PodsUnreadableNeedsCapacity_Unknown(t *testing.T) {
	base := &graph.Snapshot{
		ID: "pods-unread", Phase: graph.PhaseBaseline,
		Meta: map[string]any{
			"pods_collect_ok":                    false,
			"pods_collect_error":                 "forbidden",
			"nodes_collect_ok":                   true,
			"pod_scheduling_capacity_estimate":   100, // stale / incomplete — must not PASS
			"pdbsCollected":                      true,
		},
		Nodes: []graph.Node{
			{ID: "cluster/c", Kind: graph.KindCluster, Name: "c"},
			{ID: "node/n1", Kind: graph.KindNode, Name: "n1", Attributes: map[string]any{"allocatablePods": 110}},
			{ID: "workload/shop/api", Kind: graph.KindWorkload, Name: "api", Namespace: "shop",
				Attributes: map[string]any{"replicas": 3, "type": "Deployment"}},
		},
	}
	ch := &loader.ChangeEnvelope{
		Kind: "argo-sync", ID: "scale", Facts: map[string]any{"scenario": "argo-impact"},
		Seeds: []string{"workload/shop/api"},
		PatchNodes: []graph.Node{{
			ID:         "workload/shop/api",
			Attributes: map[string]any{"replicas": 6},
		}},
	}
	v := impact.SchedulingCapacity{}.Evaluate(schedulingCtx(base, ch))
	if v.Status != impact.Unknown {
		t.Fatalf("pods unreadable + scale want UNKNOWN (never PASS), got %s: %s", v.Status, v.Explanation)
	}
	if !strings.Contains(v.Explanation, "pod") {
		t.Fatalf("explanation should mention pods, got %q", v.Explanation)
	}
}

func TestScheduling_EnvChangeUnchangedAffinity_NoNodes_Passes(t *testing.T) {
	// Bug D: presence of affinity/requests must not force UNKNOWN when unchanged vs live.
	tpl := map[string]any{
		"spec": map[string]any{
			"affinity": map[string]any{
				"nodeAffinity": map[string]any{
					"requiredDuringSchedulingIgnoredDuringExecution": map[string]any{
						"nodeSelectorTerms": []any{
							map[string]any{"matchExpressions": []any{
								map[string]any{"key": "kubernetes.io/os", "operator": "In", "values": []any{"linux"}},
							}},
						},
					},
				},
			},
			"containers": []any{
				map[string]any{
					"name": "api",
					"env":  []any{map[string]any{"name": "NEW", "value": "1"}},
					"resources": map[string]any{
						"requests": map[string]any{"cpu": "100m", "memory": "128Mi"},
					},
				},
			},
		},
	}
	liveTpl := map[string]any{
		"spec": map[string]any{
			"affinity": map[string]any{
				"nodeAffinity": map[string]any{
					"requiredDuringSchedulingIgnoredDuringExecution": map[string]any{
						"nodeSelectorTerms": []any{
							map[string]any{"matchExpressions": []any{
								map[string]any{"key": "kubernetes.io/os", "operator": "In", "values": []any{"linux"}},
							}},
						},
					},
				},
			},
			"containers": []any{
				map[string]any{
					"name": "api",
					"resources": map[string]any{
						"requests": map[string]any{"cpu": "100m", "memory": "128Mi"},
					},
				},
			},
		},
	}
	base := &graph.Snapshot{
		ID: "env-aff", Phase: graph.PhaseBaseline,
		Meta: map[string]any{"nodes_collect_error": "forbidden", "pdbsCollected": true},
		Nodes: []graph.Node{
			{ID: "cluster/c", Kind: graph.KindCluster, Name: "c"},
			{ID: "workload/shop/api", Kind: graph.KindWorkload, Name: "api", Namespace: "shop",
				Attributes: map[string]any{
					"replicas": 3, "type": "Deployment",
					"hasAffinity": true,
					"affinity":    liveTpl["spec"].(map[string]any)["affinity"],
					"podTemplate": liveTpl,
				}},
		},
	}
	ch := &loader.ChangeEnvelope{
		Kind: "argo-sync", ID: "env", Facts: map[string]any{"scenario": "argo-impact"},
		Seeds: []string{"workload/shop/api"},
		PatchNodes: []graph.Node{{
			ID: "workload/shop/api",
			Attributes: map[string]any{
				"replicas":           3,
				"maxUnavailable":     "1",
				"hasAffinity":        true,
				"affinity":           tpl["spec"].(map[string]any)["affinity"],
				"podTemplate":        tpl,
				"podTemplateChanged": true,
			},
		}},
	}
	v := impact.SchedulingCapacity{}.Evaluate(schedulingCtx(base, ch))
	if v.Status != impact.Pass {
		t.Fatalf("env-only + unchanged affinity/requests + no nodes want PASS, got %s: %s", v.Status, v.Explanation)
	}
}

func TestScheduling_NodeSelectorChanged_NoNodes_Unknown(t *testing.T) {
	base := &graph.Snapshot{
		ID: "ns-chg", Phase: graph.PhaseBaseline,
		Meta: map[string]any{"nodes_collect_error": "forbidden"},
		Nodes: []graph.Node{
			{ID: "cluster/c", Kind: graph.KindCluster, Name: "c"},
			{ID: "workload/shop/api", Kind: graph.KindWorkload, Name: "api", Namespace: "shop",
				Attributes: map[string]any{
					"replicas": 3, "type": "Deployment",
					"nodeSelector": map[string]any{"kubernetes.io/os": "linux"},
					"podTemplate": map[string]any{
						"spec": map[string]any{
							"nodeSelector": map[string]any{"kubernetes.io/os": "linux"},
						},
					},
				}},
		},
	}
	ch := &loader.ChangeEnvelope{
		Kind: "argo-sync", ID: "ns", Facts: map[string]any{"scenario": "argo-impact"},
		Seeds: []string{"workload/shop/api"},
		PatchNodes: []graph.Node{{
			ID: "workload/shop/api",
			Attributes: map[string]any{
				"replicas":     3,
				"nodeSelector": map[string]any{"node-pool": "high-mem"},
				"podTemplate": map[string]any{
					"spec": map[string]any{
						"nodeSelector": map[string]any{"node-pool": "high-mem"},
					},
				},
			},
		}},
	}
	v := impact.SchedulingCapacity{}.Evaluate(schedulingCtx(base, ch))
	if v.Status != impact.Unknown {
		t.Fatalf("changed nodeSelector + no nodes want UNKNOWN, got %s: %s", v.Status, v.Explanation)
	}
}

func TestScheduling_UnchangedNodeSelector_NoNodes_Passes(t *testing.T) {
	ns := map[string]any{"kubernetes.io/os": "linux"}
	base := &graph.Snapshot{
		ID: "ns-same", Phase: graph.PhaseBaseline,
		Meta: map[string]any{"nodes_collect_error": "forbidden"},
		Nodes: []graph.Node{
			{ID: "cluster/c", Kind: graph.KindCluster, Name: "c"},
			{ID: "workload/shop/api", Kind: graph.KindWorkload, Name: "api", Namespace: "shop",
				Attributes: map[string]any{
					"replicas": 3, "type": "Deployment", "nodeSelector": ns,
					"podTemplate": map[string]any{"spec": map[string]any{"nodeSelector": ns}},
				}},
		},
	}
	ch := &loader.ChangeEnvelope{
		Kind: "argo-sync", ID: "ns-same", Facts: map[string]any{"scenario": "argo-impact"},
		Seeds: []string{"workload/shop/api"},
		PatchNodes: []graph.Node{{
			ID: "workload/shop/api",
			Attributes: map[string]any{
				"replicas": 3, "nodeSelector": ns,
				"podTemplate": map[string]any{
					"spec": map[string]any{
						"nodeSelector": ns,
						"containers": []any{
							map[string]any{"name": "api", "env": []any{map[string]any{"name": "X", "value": "1"}}},
						},
					},
				},
				"podTemplateChanged": true,
			},
		}},
	}
	v := impact.SchedulingCapacity{}.Evaluate(schedulingCtx(base, ch))
	if v.Status != impact.Pass {
		t.Fatalf("unchanged nodeSelector + env + no nodes want PASS, got %s: %s", v.Status, v.Explanation)
	}
}
