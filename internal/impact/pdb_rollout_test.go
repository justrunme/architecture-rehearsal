package impact_test

import (
	"strings"
	"testing"

	"github.com/justrunme/architecture-rehearsal/internal/graph"
	"github.com/justrunme/architecture-rehearsal/internal/impact"
	"github.com/justrunme/architecture-rehearsal/internal/loader"
	"github.com/justrunme/architecture-rehearsal/internal/podtemplate"
)

func pdbCtx(base *graph.Snapshot, ch *loader.ChangeEnvelope) impact.Context {
	prop := loader.ApplyChange(base, ch)
	return impact.Context{
		Baseline: base, Proposed: prop, Change: ch,
		BaseIdx: graph.BuildIndex(base), PropIdx: graph.BuildIndex(prop),
		PDBsCollected: true,
	}
}

func tpl(mut func(map[string]any)) map[string]any {
	t := map[string]any{
		"metadata": map[string]any{"labels": map[string]any{"app": "api"}},
		"spec": map[string]any{
			"containers": []any{
				map[string]any{"name": "api", "image": "nginx:1.27"},
			},
		},
	}
	if mut != nil {
		mut(t)
	}
	return t
}

func shopAPIBase(withPDB bool, template map[string]any) *graph.Snapshot {
	nodes := []graph.Node{
		{ID: "cluster/c", Kind: graph.KindCluster, Name: "c"},
		{ID: "workload/shop/api", Kind: graph.KindWorkload, Name: "api", Namespace: "shop",
			Attributes: map[string]any{
				"replicas": 3, "type": "Deployment",
				"maxUnavailable": "100%", "maxSurge": "0",
				"podLabels":      map[string]any{"app": "api"},
				"podTemplate":    template,
			}},
	}
	edges := []graph.Edge{}
	if withPDB {
		nodes = append(nodes, graph.Node{
			ID: "pdb/shop/api", Kind: graph.KindPDB, Name: "api", Namespace: "shop",
			Attributes: map[string]any{"minAvailable": 2, "matchLabels": map[string]any{"app": "api"}},
		})
		edges = append(edges, graph.Edge{From: "workload/shop/api", To: "pdb/shop/api", Rel: graph.RelProtectedBy})
	}
	return &graph.Snapshot{
		ID: "shop", Phase: graph.PhaseBaseline,
		Meta:  map[string]any{"pdbsCollected": true, "pod_scheduling_capacity_estimate": 50},
		Nodes: nodes, Edges: edges,
	}
}

func TestPDBRollout_UnchangedTemplate_Warns(t *testing.T) {
	base := shopAPIBase(true, tpl(nil))
	ch := &loader.ChangeEnvelope{
		Kind: "argo-sync", ID: "noop", Facts: map[string]any{"scenario": "argo-impact"},
	}
	v := impact.PDBRollout{}.Evaluate(pdbCtx(base, ch))
	if v.Status != impact.Warn {
		t.Fatalf("unchanged template want WARN, got %s: %s evidence=%v", v.Status, v.Explanation, v.Evidence)
	}
	if !strings.Contains(v.Explanation+" "+strings.Join(v.Evidence, " "), "existing strategy can take pods to 0 on next rollout") {
		t.Fatalf("want existing-strategy wording, got %q %v", v.Explanation, v.Evidence)
	}
}

func TestPDBRollout_ResourcesOnly_Blocks(t *testing.T) {
	base := shopAPIBase(true, tpl(nil))
	desired := tpl(func(t map[string]any) {
		t["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)["resources"] = map[string]any{
			"requests": map[string]any{"cpu": "100m"},
		}
	})
	ch := &loader.ChangeEnvelope{
		Kind: "argo-sync", ID: "resources", Facts: map[string]any{"scenario": "argo-impact"},
		Seeds: []string{"workload/shop/api"},
		PatchNodes: []graph.Node{{
			ID: "workload/shop/api",
			Attributes: map[string]any{
				"replicas": 3, "maxUnavailable": "100%", "maxSurge": "0",
				"podTemplate": desired, "podTemplateChanged": true,
			},
		}},
	}
	v := impact.PDBRollout{}.Evaluate(pdbCtx(base, ch))
	if v.Status != impact.Block {
		t.Fatalf("resources-only want BLOCK, got %s: %s", v.Status, v.Explanation)
	}
}

func TestPDBRollout_ProbeOnly_Blocks(t *testing.T) {
	base := shopAPIBase(true, tpl(nil))
	desired := tpl(func(t map[string]any) {
		t["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)["readinessProbe"] = map[string]any{
			"httpGet": map[string]any{"path": "/ready", "port": 8080},
		}
	})
	ch := &loader.ChangeEnvelope{
		Kind: "argo-sync", ID: "probe", Facts: map[string]any{"scenario": "argo-impact"},
		Seeds: []string{"workload/shop/api"},
		PatchNodes: []graph.Node{{
			ID: "workload/shop/api",
			Attributes: map[string]any{
				"replicas": 3, "maxUnavailable": "100%", "maxSurge": "0",
				"podTemplate": desired, "podTemplateChanged": true,
			},
		}},
	}
	v := impact.PDBRollout{}.Evaluate(pdbCtx(base, ch))
	if v.Status != impact.Block {
		t.Fatalf("probe-only want BLOCK, got %s: %s", v.Status, v.Explanation)
	}
}

func TestPDBRollout_AnnotationOnly_Blocks(t *testing.T) {
	base := shopAPIBase(true, tpl(nil))
	desired := tpl(func(t map[string]any) {
		t["metadata"].(map[string]any)["annotations"] = map[string]any{"roll": "1"}
	})
	ch := &loader.ChangeEnvelope{
		Kind: "argo-sync", ID: "annot", Facts: map[string]any{"scenario": "argo-impact"},
		Seeds: []string{"workload/shop/api"},
		PatchNodes: []graph.Node{{
			ID: "workload/shop/api",
			Attributes: map[string]any{
				"replicas": 3, "maxUnavailable": "100%", "maxSurge": "0",
				"podTemplate": desired, "podTemplateChanged": true,
			},
		}},
	}
	v := impact.PDBRollout{}.Evaluate(pdbCtx(base, ch))
	if v.Status != impact.Block {
		t.Fatalf("annotation-only want BLOCK, got %s: %s", v.Status, v.Explanation)
	}
}

func TestPDBRollout_MissingLiveTemplate_Blocks(t *testing.T) {
	base := shopAPIBase(true, nil) // no podTemplate on live
	desired := tpl(nil)
	ch := &loader.ChangeEnvelope{
		Kind: "argo-sync", ID: "missing", Facts: map[string]any{"scenario": "argo-impact"},
		Seeds: []string{"workload/shop/api"},
		PatchNodes: []graph.Node{{
			ID: "workload/shop/api",
			Attributes: map[string]any{
				"replicas": 3, "maxUnavailable": "100%", "maxSurge": "0",
				"podTemplate": desired,
				// no podTemplateChanged — detector falls back to subset fail-closed
			},
		}},
	}
	v := impact.PDBRollout{}.Evaluate(pdbCtx(base, ch))
	if v.Status != impact.Block {
		t.Fatalf("missing live template want BLOCK, got %s: %s", v.Status, v.Explanation)
	}
	if !podtemplate.SubsetChanged(desired, nil) {
		t.Fatal("precondition")
	}
}

func TestPDBRollout_LiveDefaultsVsDesired_Warns(t *testing.T) {
	live := tpl(func(t map[string]any) {
		c := t["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)
		c["imagePullPolicy"] = "IfNotPresent"
		c["readinessProbe"] = map[string]any{
			"httpGet": map[string]any{"path": "/", "port": "http", "scheme": "HTTP"},
		}
		t["spec"].(map[string]any)["restartPolicy"] = "Always"
	})
	desired := tpl(func(t map[string]any) {
		c := t["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)
		c["readinessProbe"] = map[string]any{
			"httpGet": map[string]any{"path": "/", "port": "http"},
		}
	})
	if podtemplate.SubsetChanged(desired, live) {
		t.Fatal("precondition: subset must treat live defaults as unchanged")
	}
	base := shopAPIBase(true, live)
	ch := &loader.ChangeEnvelope{
		Kind: "argo-sync", ID: "defaults", Facts: map[string]any{"scenario": "argo-impact"},
		Seeds: []string{"workload/shop/api"},
		PatchNodes: []graph.Node{{
			ID: "workload/shop/api",
			Attributes: map[string]any{
				"replicas": 3, "maxUnavailable": "100%", "maxSurge": "0",
				"podTemplate": desired, "podTemplateChanged": false,
			},
		}},
	}
	v := impact.PDBRollout{}.Evaluate(pdbCtx(base, ch))
	if v.Status != impact.Warn {
		t.Fatalf("live defaults vs same desired want WARN, got %s: %s", v.Status, v.Explanation)
	}
}

func TestPDBRollout_EnvChange_Blocks(t *testing.T) {
	base := shopAPIBase(true, tpl(nil))
	desired := tpl(func(t map[string]any) {
		t["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)["env"] = []any{
			map[string]any{"name": "FEATURE_FLAGS", "value": "fast-checkout=on"},
		}
	})
	ch := &loader.ChangeEnvelope{
		Kind: "argo-sync", ID: "env", Facts: map[string]any{"scenario": "argo-impact"},
		Seeds: []string{"workload/shop/api"},
		PatchNodes: []graph.Node{{
			ID: "workload/shop/api",
			Attributes: map[string]any{
				"replicas": 3, "maxUnavailable": "100%", "maxSurge": "0",
				"podTemplate": desired, "podTemplateChanged": true,
			},
		}},
	}
	v := impact.PDBRollout{}.Evaluate(pdbCtx(base, ch))
	if v.Status != impact.Block {
		t.Fatalf("env change want BLOCK, got %s: %s", v.Status, v.Explanation)
	}
}

func TestPDBRollout_ImageChange_Blocks(t *testing.T) {
	base := shopAPIBase(true, tpl(nil))
	desired := tpl(func(t map[string]any) {
		t["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)["image"] = "nginx:1.28"
	})
	ch := &loader.ChangeEnvelope{
		Kind: "argo-sync", ID: "image", Facts: map[string]any{"scenario": "argo-impact"},
		Seeds: []string{"workload/shop/api"},
		PatchNodes: []graph.Node{{
			ID: "workload/shop/api",
			Attributes: map[string]any{
				"replicas": 3, "maxUnavailable": "100%", "maxSurge": "0",
				"podTemplate": desired, "podTemplateChanged": true,
			},
		}},
	}
	v := impact.PDBRollout{}.Evaluate(pdbCtx(base, ch))
	if v.Status != impact.Block {
		t.Fatalf("image change want BLOCK, got %s: %s", v.Status, v.Explanation)
	}
}

func TestPDBRollout_BelowMinAvailable_BlocksWithWording(t *testing.T) {
	base := shopAPIBase(true, tpl(nil))
	base.Nodes[1].Attributes["maxUnavailable"] = "1"
	desired := tpl(func(t map[string]any) {
		t["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)["env"] = []any{
			map[string]any{"name": "FEATURE_FLAGS", "value": "on"},
		}
	})
	ch := &loader.ChangeEnvelope{
		Kind: "argo-sync", ID: "99pct", Facts: map[string]any{"scenario": "argo-impact"},
		Seeds: []string{"workload/shop/api"},
		PatchNodes: []graph.Node{{
			ID: "workload/shop/api",
			Attributes: map[string]any{
				"replicas": 3, "maxUnavailable": "99%",
				"podTemplate": desired, "podTemplateChanged": true,
			},
		}},
	}
	v := impact.PDBRollout{}.Evaluate(pdbCtx(base, ch))
	if v.Status != impact.Block {
		t.Fatalf("below minAvailable want BLOCK, got %s: %s", v.Status, v.Explanation)
	}
	want := "rollout can drop available pods to 1, below the minAvailable=2 your PDB declares (PDBs do not enforce this during rollouts)"
	blob := v.Explanation + " " + strings.Join(v.Evidence, " ")
	if !strings.Contains(blob, want) {
		t.Fatalf("want %q, got %q", want, blob)
	}
}

func TestPDBRollout_MaxUnavailableOne_Passes(t *testing.T) {
	base := shopAPIBase(true, tpl(nil))
	base.Nodes[1].Attributes["maxUnavailable"] = "1"
	base.Nodes[1].Attributes["maxSurge"] = "1"
	desired := tpl(func(t map[string]any) {
		t["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)["env"] = []any{
			map[string]any{"name": "FEATURE_FLAGS", "value": "on"},
		}
	})
	ch := &loader.ChangeEnvelope{
		Kind: "argo-sync", ID: "fp3", Facts: map[string]any{"scenario": "argo-impact"},
		Seeds: []string{"workload/shop/api"},
		PatchNodes: []graph.Node{{
			ID: "workload/shop/api",
			Attributes: map[string]any{
				"replicas": 3, "maxUnavailable": "1", "maxSurge": "1",
				"podTemplate": desired, "podTemplateChanged": true,
			},
		}},
	}
	v := impact.PDBRollout{}.Evaluate(pdbCtx(base, ch))
	if v.Status != impact.Pass {
		t.Fatalf("maxUnavailable:1 want PASS, got %s: %s", v.Status, v.Explanation)
	}
}

func TestPDBRollout_Default25PercentOf3_DoesNotFalseBlock(t *testing.T) {
	base := shopAPIBase(true, tpl(nil))
	delete(base.Nodes[1].Attributes, "maxUnavailable")
	delete(base.Nodes[1].Attributes, "maxSurge")
	desired := tpl(func(t map[string]any) {
		t["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)["env"] = []any{
			map[string]any{"name": "FEATURE_FLAGS", "value": "on"},
		}
	})
	ch := &loader.ChangeEnvelope{
		Kind: "argo-sync", ID: "default", Facts: map[string]any{"scenario": "argo-impact"},
		Seeds: []string{"workload/shop/api"},
		PatchNodes: []graph.Node{{
			ID: "workload/shop/api",
			Attributes: map[string]any{
				"replicas": 3, "podTemplate": desired, "podTemplateChanged": true,
			},
		}},
	}
	v := impact.PDBRollout{}.Evaluate(pdbCtx(base, ch))
	if v.Status != impact.Pass {
		t.Fatalf("default 25pct want PASS, got %s: %s evidence=%v", v.Status, v.Explanation, v.Evidence)
	}
}
