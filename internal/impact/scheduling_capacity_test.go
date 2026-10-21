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
	// Node allocatable 110; 108 pods occupy slots. Plain scale 3→6 needs delta=3 > free=2 → BLOCK.
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
	for i := 0; i < 108; i++ {
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
				"replicas": 6, "maxSurge": "25%", "podTemplateChanged": false,
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

func TestScheduling_CPUHogBlocksScale(t *testing.T) {
	// Plain scale (no template change): only delta pods. Hog leaves 100m free; 3×150m needs 450 → BLOCK.
	tpl := map[string]any{
		"spec": map[string]any{
			"containers": []any{
				map[string]any{
					"name": "api",
					"resources": map[string]any{
						"requests": map[string]any{"cpu": "150m", "memory": "64Mi"},
					},
				},
			},
		},
	}
	base := &graph.Snapshot{
		ID: "cpuhog", Phase: graph.PhaseBaseline,
		Meta: map[string]any{"pods_collect_ok": true, "nodes_collect_ok": true, "pdbsCollected": true},
		Nodes: []graph.Node{
			{ID: "cluster/c", Kind: graph.KindCluster, Name: "c"},
			{ID: "node/n1", Kind: graph.KindNode, Name: "n1", Attributes: map[string]any{
				"allocatablePods":        110,
				"allocatableCPUMillis":   8000,
				"allocatableMemoryBytes": 16 * 1024 * 1024 * 1024,
				"labels":                 map[string]any{"kubernetes.io/os": "linux"},
			}},
			{ID: "workload/shop/api", Kind: graph.KindWorkload, Name: "api", Namespace: "shop",
				Attributes: map[string]any{
					"replicas": 3, "type": "Deployment", "maxSurge": "25%", "podTemplate": tpl,
				}},
			{ID: "pod/kube-system/kube-proxy", Kind: graph.KindPod, Namespace: "kube-system", Name: "kube-proxy",
				Attributes: map[string]any{"phase": "Running", "nodeName": "n1"}},
			{ID: "pod/other/hog", Kind: graph.KindPod, Namespace: "other", Name: "hog",
				Attributes: map[string]any{"phase": "Running", "nodeName": "n1", "cpuRequestMillis": 7450}},
			{ID: "pod/shop/api-0", Kind: graph.KindPod, Namespace: "shop", Name: "api-0",
				Attributes: map[string]any{"phase": "Running", "nodeName": "n1", "cpuRequestMillis": 150}},
			{ID: "pod/shop/api-1", Kind: graph.KindPod, Namespace: "shop", Name: "api-1",
				Attributes: map[string]any{"phase": "Running", "nodeName": "n1", "cpuRequestMillis": 150}},
			{ID: "pod/shop/api-2", Kind: graph.KindPod, Namespace: "shop", Name: "api-2",
				Attributes: map[string]any{"phase": "Running", "nodeName": "n1", "cpuRequestMillis": 150}},
		},
	}
	ch := &loader.ChangeEnvelope{
		Kind: "argo-sync", ID: "scale-cpu", Facts: map[string]any{"scenario": "argo-impact"},
		Seeds: []string{"workload/shop/api"},
		PatchNodes: []graph.Node{{
			ID: "workload/shop/api",
			Attributes: map[string]any{
				"replicas": 6, "maxSurge": "25%", "podTemplate": tpl, "podTemplateChanged": false,
			},
		}},
	}
	v := impact.SchedulingCapacity{}.Evaluate(schedulingCtx(base, ch))
	if v.Status != impact.Block {
		t.Fatalf("CPU hog + plain scale want BLOCK, got %s: %s evidence=%v", v.Status, v.Explanation, v.Evidence)
	}
}

func TestScheduling_CPUAllocUnreadable_Unknown(t *testing.T) {
	base := &graph.Snapshot{
		ID: "cpu-unk", Phase: graph.PhaseBaseline,
		Meta: map[string]any{"pods_collect_ok": true, "pdbsCollected": true},
		Nodes: []graph.Node{
			{ID: "cluster/c", Kind: graph.KindCluster, Name: "c"},
			{ID: "node/n1", Kind: graph.KindNode, Name: "n1", Attributes: map[string]any{
				"allocatablePods": 110,
				// no allocatableCPUMillis
				"labels": map[string]any{"kubernetes.io/os": "linux"},
			}},
			{ID: "workload/shop/api", Kind: graph.KindWorkload, Name: "api", Namespace: "shop",
				Attributes: map[string]any{"replicas": 3, "type": "Deployment", "maxSurge": "25%"}},
			{ID: "pod/shop/api-0", Kind: graph.KindPod, Attributes: map[string]any{"phase": "Running"}},
		},
	}
	ch := &loader.ChangeEnvelope{
		Kind: "argo-sync", ID: "scale", Facts: map[string]any{"scenario": "argo-impact"},
		Seeds: []string{"workload/shop/api"},
		PatchNodes: []graph.Node{{
			ID: "workload/shop/api",
			Attributes: map[string]any{
				"replicas": 6, "maxSurge": "25%",
				"podTemplate": map[string]any{
					"spec": map[string]any{
						"containers": []any{
							map[string]any{
								"name": "api",
								"resources": map[string]any{
									"requests": map[string]any{"cpu": "150m"},
								},
							},
						},
					},
				},
			},
		}},
	}
	v := impact.SchedulingCapacity{}.Evaluate(schedulingCtx(base, ch))
	if v.Status != impact.Unknown {
		t.Fatalf("missing allocatable CPU + scale with requests want UNKNOWN, got %s: %s", v.Status, v.Explanation)
	}
}

func TestScheduling_G_TemplateChangeCountsSurge(t *testing.T) {
	// Env change, same replicas, maxSurge 1, only 100m free → BLOCK (surge pod needs 200m).
	tplLive := map[string]any{"spec": map[string]any{
		"containers": []any{map[string]any{"name": "api", "resources": map[string]any{
			"requests": map[string]any{"cpu": "200m"},
		}}},
	}}
	tplDesired := map[string]any{"spec": map[string]any{
		"containers": []any{map[string]any{
			"name": "api",
			"env":  []any{map[string]any{"name": "X", "value": "1"}},
			"resources": map[string]any{"requests": map[string]any{"cpu": "200m"}},
		}},
	}}
	base := &graph.Snapshot{
		ID: "g-surge", Phase: graph.PhaseBaseline,
		Meta: map[string]any{"pods_collect_ok": true, "pdbsCollected": true},
		Nodes: []graph.Node{
			{ID: "cluster/c", Kind: graph.KindCluster, Name: "c"},
			{ID: "node/n1", Kind: graph.KindNode, Name: "n1", Attributes: map[string]any{
				"allocatablePods": 110, "allocatableCPUMillis": 1000,
				"labels": map[string]any{"kubernetes.io/os": "linux"},
			}},
			{ID: "workload/shop/api", Kind: graph.KindWorkload, Name: "api", Namespace: "shop",
				Attributes: map[string]any{
					"replicas": 3, "type": "Deployment", "maxSurge": "1", "maxUnavailable": "0",
					"podTemplate": tplLive,
				}},
			{ID: "pod/other/hog", Kind: graph.KindPod, Attributes: map[string]any{
				"phase": "Running", "nodeName": "n1", "cpuRequestMillis": 300,
			}},
			{ID: "pod/shop/a0", Kind: graph.KindPod, Attributes: map[string]any{"phase": "Running", "nodeName": "n1", "cpuRequestMillis": 200}},
			{ID: "pod/shop/a1", Kind: graph.KindPod, Attributes: map[string]any{"phase": "Running", "nodeName": "n1", "cpuRequestMillis": 200}},
			{ID: "pod/shop/a2", Kind: graph.KindPod, Attributes: map[string]any{"phase": "Running", "nodeName": "n1", "cpuRequestMillis": 200}},
		},
	}
	// free = 1000-300-600 = 100m
	ch := &loader.ChangeEnvelope{
		Kind: "argo-sync", ID: "env", Facts: map[string]any{"scenario": "argo-impact"},
		Seeds: []string{"workload/shop/api"},
		PatchNodes: []graph.Node{{
			ID: "workload/shop/api",
			Attributes: map[string]any{
				"replicas": 3, "maxSurge": "1", "maxUnavailable": "0",
				"podTemplate": tplDesired, "podTemplateChanged": true,
			},
		}},
	}
	v := impact.SchedulingCapacity{}.Evaluate(schedulingCtx(base, ch))
	if v.Status != impact.Block {
		t.Fatalf("G: template change + surge with 100m free want BLOCK, got %s: %s %v", v.Status, v.Explanation, v.Evidence)
	}
}

func TestScheduling_H_PlainScaleNoSurge(t *testing.T) {
	// Scale 3→4, unchanged template, exactly 200m free → PASS (only 1 new pod).
	tpl := map[string]any{"spec": map[string]any{
		"containers": []any{map[string]any{"name": "api", "resources": map[string]any{
			"requests": map[string]any{"cpu": "200m"},
		}}},
	}}
	base := &graph.Snapshot{
		ID: "h-scale", Phase: graph.PhaseBaseline,
		Meta: map[string]any{"pods_collect_ok": true, "pdbsCollected": true},
		Nodes: []graph.Node{
			{ID: "cluster/c", Kind: graph.KindCluster, Name: "c"},
			{ID: "node/n1", Kind: graph.KindNode, Name: "n1", Attributes: map[string]any{
				"allocatablePods": 110, "allocatableCPUMillis": 800,
				"labels": map[string]any{"kubernetes.io/os": "linux"},
			}},
			{ID: "workload/shop/api", Kind: graph.KindWorkload, Name: "api", Namespace: "shop",
				Attributes: map[string]any{
					"replicas": 3, "type": "Deployment", "maxSurge": "1", "podTemplate": tpl,
				}},
			{ID: "pod/shop/a0", Kind: graph.KindPod, Attributes: map[string]any{"phase": "Running", "nodeName": "n1", "cpuRequestMillis": 200}},
			{ID: "pod/shop/a1", Kind: graph.KindPod, Attributes: map[string]any{"phase": "Running", "nodeName": "n1", "cpuRequestMillis": 200}},
			{ID: "pod/shop/a2", Kind: graph.KindPod, Attributes: map[string]any{"phase": "Running", "nodeName": "n1", "cpuRequestMillis": 200}},
		},
	}
	// free = 200m
	ch := &loader.ChangeEnvelope{
		Kind: "argo-sync", ID: "scale", Facts: map[string]any{"scenario": "argo-impact"},
		Seeds: []string{"workload/shop/api"},
		PatchNodes: []graph.Node{{
			ID: "workload/shop/api",
			Attributes: map[string]any{
				"replicas": 4, "maxSurge": "1", "podTemplate": tpl, "podTemplateChanged": false,
			},
		}},
	}
	v := impact.SchedulingCapacity{}.Evaluate(schedulingCtx(base, ch))
	if v.Status != impact.Pass {
		t.Fatalf("H: plain scale with exact free want PASS, got %s: %s %v", v.Status, v.Explanation, v.Evidence)
	}
}

func TestScheduling_PerNodeFragmentationBlocks(t *testing.T) {
	// Two nodes with 100m free each cannot fit one 150m pod.
	base := &graph.Snapshot{
		ID: "frag", Phase: graph.PhaseBaseline,
		Meta: map[string]any{"pods_collect_ok": true, "pdbsCollected": true},
		Nodes: []graph.Node{
			{ID: "cluster/c", Kind: graph.KindCluster, Name: "c"},
			{ID: "node/n1", Kind: graph.KindNode, Name: "n1", Attributes: map[string]any{
				"allocatablePods": 10, "allocatableCPUMillis": 500,
				"labels": map[string]any{"kubernetes.io/os": "linux"},
			}},
			{ID: "node/n2", Kind: graph.KindNode, Name: "n2", Attributes: map[string]any{
				"allocatablePods": 10, "allocatableCPUMillis": 500,
				"labels": map[string]any{"kubernetes.io/os": "linux"},
			}},
			{ID: "workload/shop/api", Kind: graph.KindWorkload, Name: "api", Namespace: "shop",
				Attributes: map[string]any{"replicas": 1, "type": "Deployment", "maxSurge": "0"}},
			{ID: "pod/n1/hog", Kind: graph.KindPod, Attributes: map[string]any{"phase": "Running", "nodeName": "n1", "cpuRequestMillis": 400}},
			{ID: "pod/n2/hog", Kind: graph.KindPod, Attributes: map[string]any{"phase": "Running", "nodeName": "n2", "cpuRequestMillis": 400}},
			{ID: "pod/shop/api", Kind: graph.KindPod, Attributes: map[string]any{"phase": "Running", "nodeName": "n1", "cpuRequestMillis": 0}},
		},
	}
	ch := &loader.ChangeEnvelope{
		Kind: "argo-sync", ID: "scale", Facts: map[string]any{"scenario": "argo-impact"},
		Seeds: []string{"workload/shop/api"},
		PatchNodes: []graph.Node{{
			ID: "workload/shop/api",
			Attributes: map[string]any{
				"replicas": 2, "maxSurge": "0", "podTemplateChanged": false,
				"podTemplate": map[string]any{"spec": map[string]any{
					"containers": []any{map[string]any{"name": "api", "resources": map[string]any{
						"requests": map[string]any{"cpu": "150m"},
					}}},
				}},
			},
		}},
	}
	v := impact.SchedulingCapacity{}.Evaluate(schedulingCtx(base, ch))
	if v.Status != impact.Block {
		t.Fatalf("fragmentation: 2×100m free cannot fit 150m want BLOCK, got %s: %s %v", v.Status, v.Explanation, v.Evidence)
	}
}

func TestScheduling_InitContainerEffectiveRequest(t *testing.T) {
	// init 500m, containers 100+100=200 → effective 500m; free 400 → BLOCK on scale +1.
	tpl := map[string]any{"spec": map[string]any{
		"initContainers": []any{map[string]any{"name": "init", "resources": map[string]any{
			"requests": map[string]any{"cpu": "500m"},
		}}},
		"containers": []any{
			map[string]any{"name": "a", "resources": map[string]any{"requests": map[string]any{"cpu": "100m"}}},
			map[string]any{"name": "b", "resources": map[string]any{"requests": map[string]any{"cpu": "100m"}}},
		},
	}}
	base := &graph.Snapshot{
		ID: "init", Phase: graph.PhaseBaseline,
		Meta: map[string]any{"pods_collect_ok": true, "pdbsCollected": true},
		Nodes: []graph.Node{
			{ID: "cluster/c", Kind: graph.KindCluster, Name: "c"},
			{ID: "node/n1", Kind: graph.KindNode, Name: "n1", Attributes: map[string]any{
				"allocatablePods": 10, "allocatableCPUMillis": 1000,
				"labels": map[string]any{"kubernetes.io/os": "linux"},
			}},
			{ID: "workload/shop/api", Kind: graph.KindWorkload, Name: "api", Namespace: "shop",
				Attributes: map[string]any{"replicas": 1, "type": "Deployment", "maxSurge": "0", "podTemplate": tpl}},
			{ID: "pod/shop/api", Kind: graph.KindPod, Attributes: map[string]any{
				"phase": "Running", "nodeName": "n1", "cpuRequestMillis": 500,
			}},
			{ID: "pod/other/hog", Kind: graph.KindPod, Attributes: map[string]any{
				"phase": "Running", "nodeName": "n1", "cpuRequestMillis": 100,
			}},
		},
	}
	// free = 400m; new pod needs 500m
	ch := &loader.ChangeEnvelope{
		Kind: "argo-sync", ID: "scale", Facts: map[string]any{"scenario": "argo-impact"},
		Seeds: []string{"workload/shop/api"},
		PatchNodes: []graph.Node{{
			ID: "workload/shop/api",
			Attributes: map[string]any{"replicas": 2, "maxSurge": "0", "podTemplate": tpl, "podTemplateChanged": false},
		}},
	}
	v := impact.SchedulingCapacity{}.Evaluate(schedulingCtx(base, ch))
	if v.Status != impact.Block {
		t.Fatalf("init effective 500m with 400 free want BLOCK, got %s: %s", v.Status, v.Explanation)
	}
}
