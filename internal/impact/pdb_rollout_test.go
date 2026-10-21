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

func templateHash(mut func(map[string]any)) string {
	tpl := map[string]any{
		"metadata": map[string]any{"labels": map[string]any{"app": "api"}},
		"spec": map[string]any{
			"containers": []any{
				map[string]any{"name": "api", "image": "nginx:1.27"},
			},
		},
	}
	if mut != nil {
		mut(tpl)
	}
	return podtemplate.Hash(tpl)
}

func shopAPIBase(withPDB bool, hash string) *graph.Snapshot {
	nodes := []graph.Node{
		{ID: "cluster/c", Kind: graph.KindCluster, Name: "c"},
		{ID: "workload/shop/api", Kind: graph.KindWorkload, Name: "api", Namespace: "shop",
			Attributes: map[string]any{
				"replicas": 3, "type": "Deployment",
				"maxUnavailable":  "100%",
				"maxSurge":        "0",
				"podLabels":       map[string]any{"app": "api"},
				"podTemplateHash": hash,
				"containerImages": "api=nginx:1.27",
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

func patchTemplate(hash string, extra map[string]any) graph.Node {
	attrs := map[string]any{
		"replicas":           3,
		"maxUnavailable":     "100%",
		"maxSurge":           "0",
		"podTemplateHash":    hash,
		"containerImages":    "api=nginx:1.27",
		"podTemplateChanged": true,
	}
	for k, v := range extra {
		attrs[k] = v
	}
	return graph.Node{ID: "workload/shop/api", Attributes: attrs}
}

func TestPDBRollout_UnchangedTemplate_Warns(t *testing.T) {
	h := templateHash(nil)
	base := shopAPIBase(true, h)
	ch := &loader.ChangeEnvelope{
		Kind: "argo-sync", ID: "noop", Facts: map[string]any{"scenario": "argo-impact"},
	}
	v := impact.PDBRollout{}.Evaluate(pdbCtx(base, ch))
	if v.Status != impact.Warn {
		t.Fatalf("unchanged template want WARN, got %s: %s evidence=%v", v.Status, v.Explanation, v.Evidence)
	}
	blob := v.Explanation + " " + strings.Join(v.Evidence, " ")
	if !strings.Contains(blob, "existing strategy can take pods to 0 on next rollout") {
		t.Fatalf("want existing-strategy WARN wording, got %q", blob)
	}
}

func TestPDBRollout_ResourcesOnly_Blocks(t *testing.T) {
	baseHash := templateHash(nil)
	newHash := templateHash(func(tpl map[string]any) {
		tpl["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)["resources"] = map[string]any{
			"requests": map[string]any{"cpu": "100m"},
		}
	})
	base := shopAPIBase(true, baseHash)
	ch := &loader.ChangeEnvelope{
		Kind: "argo-sync", ID: "resources", Facts: map[string]any{"scenario": "argo-impact"},
		Seeds:      []string{"workload/shop/api"},
		PatchNodes: []graph.Node{patchTemplate(newHash, nil)},
	}
	v := impact.PDBRollout{}.Evaluate(pdbCtx(base, ch))
	if v.Status != impact.Block {
		t.Fatalf("resources-only template change want BLOCK, got %s: %s", v.Status, v.Explanation)
	}
}

func TestPDBRollout_ProbeOnly_Blocks(t *testing.T) {
	baseHash := templateHash(nil)
	newHash := templateHash(func(tpl map[string]any) {
		tpl["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)["readinessProbe"] = map[string]any{
			"httpGet": map[string]any{"path": "/ready", "port": 8080},
		}
	})
	base := shopAPIBase(true, baseHash)
	ch := &loader.ChangeEnvelope{
		Kind: "argo-sync", ID: "probe", Facts: map[string]any{"scenario": "argo-impact"},
		Seeds:      []string{"workload/shop/api"},
		PatchNodes: []graph.Node{patchTemplate(newHash, nil)},
	}
	v := impact.PDBRollout{}.Evaluate(pdbCtx(base, ch))
	if v.Status != impact.Block {
		t.Fatalf("probe-only template change want BLOCK, got %s: %s", v.Status, v.Explanation)
	}
}

func TestPDBRollout_AnnotationOnly_Blocks(t *testing.T) {
	baseHash := templateHash(nil)
	newHash := templateHash(func(tpl map[string]any) {
		tpl["metadata"].(map[string]any)["annotations"] = map[string]any{"roll": "1"}
	})
	base := shopAPIBase(true, baseHash)
	ch := &loader.ChangeEnvelope{
		Kind: "argo-sync", ID: "annot", Facts: map[string]any{"scenario": "argo-impact"},
		Seeds:      []string{"workload/shop/api"},
		PatchNodes: []graph.Node{patchTemplate(newHash, nil)},
	}
	v := impact.PDBRollout{}.Evaluate(pdbCtx(base, ch))
	if v.Status != impact.Block {
		t.Fatalf("annotation-only template change want BLOCK, got %s: %s", v.Status, v.Explanation)
	}
}

func TestPDBRollout_MissingBaselineHash_Blocks(t *testing.T) {
	newHash := templateHash(nil)
	base := shopAPIBase(true, "") // missing hash on live snapshot
	ch := &loader.ChangeEnvelope{
		Kind: "argo-sync", ID: "missing-hash", Facts: map[string]any{"scenario": "argo-impact"},
		Seeds: []string{"workload/shop/api"},
		PatchNodes: []graph.Node{{
			ID: "workload/shop/api",
			Attributes: map[string]any{
				"replicas": 3, "maxUnavailable": "100%", "maxSurge": "0",
				"podTemplateHash": newHash,
				// no podTemplateChanged flag — detector must fail-closed on missing baseline hash
			},
		}},
	}
	v := impact.PDBRollout{}.Evaluate(pdbCtx(base, ch))
	if v.Status != impact.Block {
		t.Fatalf("missing baseline hash want BLOCK (fail-closed), got %s: %s", v.Status, v.Explanation)
	}
}

func TestPDBRollout_LiveDefaultsVsDesired_Warns(t *testing.T) {
	desired := map[string]any{
		"metadata": map[string]any{"labels": map[string]any{"app": "api"}},
		"spec": map[string]any{
			"containers": []any{
				map[string]any{"name": "api", "image": "nginx:1.27"},
			},
		},
	}
	live := map[string]any{
		"metadata": map[string]any{
			"creationTimestamp": nil,
			"labels":            map[string]any{"app": "api"},
		},
		"spec": map[string]any{
			"restartPolicy":                 "Always",
			"dnsPolicy":                     "ClusterFirst",
			"schedulerName":                 "default-scheduler",
			"terminationGracePeriodSeconds": float64(30),
			"securityContext":               map[string]any{},
			"containers": []any{
				map[string]any{
					"name":                     "api",
					"image":                    "nginx:1.27",
					"imagePullPolicy":          "IfNotPresent",
					"terminationMessagePath":   "/dev/termination-log",
					"terminationMessagePolicy": "File",
					"resources":                map[string]any{},
				},
			},
		},
	}
	dh := podtemplate.Hash(desired)
	lh := podtemplate.Hash(live)
	if dh != lh {
		t.Fatalf("precondition: hashes must match after normalize; d=%s l=%s", dh, lh)
	}
	base := shopAPIBase(true, lh)
	ch := &loader.ChangeEnvelope{
		Kind: "argo-sync", ID: "defaults", Facts: map[string]any{"scenario": "argo-impact"},
		// Strategy-only patch; same template hash as live (defaults stripped).
		Seeds: []string{"workload/shop/api"},
		PatchNodes: []graph.Node{{
			ID: "workload/shop/api",
			Attributes: map[string]any{
				"replicas": 3, "maxUnavailable": "100%", "maxSurge": "0",
				"podTemplateHash": dh,
			},
		}},
	}
	v := impact.PDBRollout{}.Evaluate(pdbCtx(base, ch))
	if v.Status != impact.Warn {
		t.Fatalf("live defaults vs same desired want WARN, got %s: %s evidence=%v",
			v.Status, v.Explanation, v.Evidence)
	}
}

func TestPDBRollout_EnvChange_Blocks(t *testing.T) {
	baseHash := templateHash(nil)
	newHash := templateHash(func(tpl map[string]any) {
		tpl["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)["env"] = []any{
			map[string]any{"name": "FEATURE_FLAGS", "value": "fast-checkout=on"},
		}
	})
	base := shopAPIBase(true, baseHash)
	ch := &loader.ChangeEnvelope{
		Kind: "argo-sync", ID: "env", Facts: map[string]any{"scenario": "argo-impact"},
		Seeds: []string{"workload/shop/api"},
		PatchNodes: []graph.Node{patchTemplate(newHash, map[string]any{
			"containerEnv": "api:FEATURE_FLAGS=fast-checkout=on",
		})},
	}
	v := impact.PDBRollout{}.Evaluate(pdbCtx(base, ch))
	if v.Status != impact.Block {
		t.Fatalf("env template change want BLOCK, got %s: %s", v.Status, v.Explanation)
	}
}

func TestPDBRollout_ImageChange_Blocks(t *testing.T) {
	baseHash := templateHash(nil)
	newHash := templateHash(func(tpl map[string]any) {
		tpl["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)["image"] = "nginx:1.28"
	})
	base := shopAPIBase(true, baseHash)
	ch := &loader.ChangeEnvelope{
		Kind: "argo-sync", ID: "image", Facts: map[string]any{"scenario": "argo-impact"},
		Seeds: []string{"workload/shop/api"},
		PatchNodes: []graph.Node{patchTemplate(newHash, map[string]any{
			"containerImages": "api=nginx:1.28",
		})},
	}
	v := impact.PDBRollout{}.Evaluate(pdbCtx(base, ch))
	if v.Status != impact.Block {
		t.Fatalf("image template change want BLOCK, got %s: %s", v.Status, v.Explanation)
	}
}

func TestPDBRollout_BelowMinAvailable_BlocksWithWording(t *testing.T) {
	baseHash := templateHash(nil)
	newHash := templateHash(func(tpl map[string]any) {
		tpl["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)["env"] = []any{
			map[string]any{"name": "FEATURE_FLAGS", "value": "on"},
		}
	})
	base := &graph.Snapshot{
		ID: "x2", Phase: graph.PhaseBaseline,
		Meta: map[string]any{"pdbsCollected": true},
		Nodes: []graph.Node{
			{ID: "workload/shop/api", Kind: graph.KindWorkload, Name: "api", Namespace: "shop",
				Attributes: map[string]any{
					"replicas": 3, "type": "Deployment",
					"podTemplateHash": baseHash,
					"podLabels":       map[string]any{"app": "api"},
				}},
			{ID: "pdb/shop/api", Kind: graph.KindPDB, Name: "api", Namespace: "shop",
				Attributes: map[string]any{"minAvailable": 2, "matchLabels": map[string]any{"app": "api"}}},
		},
		Edges: []graph.Edge{{From: "workload/shop/api", To: "pdb/shop/api", Rel: graph.RelProtectedBy}},
	}
	ch := &loader.ChangeEnvelope{
		Kind: "argo-sync", ID: "99pct", Facts: map[string]any{"scenario": "argo-impact"},
		Seeds: []string{"workload/shop/api"},
		PatchNodes: []graph.Node{{
			ID: "workload/shop/api",
			Attributes: map[string]any{
				"replicas": 3, "maxUnavailable": "99%",
				"podTemplateHash": newHash, "podTemplateChanged": true,
			},
		}},
	}
	v := impact.PDBRollout{}.Evaluate(pdbCtx(base, ch))
	if v.Status != impact.Block {
		t.Fatalf("below minAvailable want BLOCK, got %s: %s evidence=%v", v.Status, v.Explanation, v.Evidence)
	}
	blob := v.Explanation + " " + strings.Join(v.Evidence, " ")
	want := "rollout can drop available pods to 1, below the minAvailable=2 your PDB declares (PDBs do not enforce this during rollouts)"
	if !strings.Contains(blob, want) {
		t.Fatalf("want wording %q, got %q", want, blob)
	}
}

func TestPDBRollout_MaxUnavailableOne_Passes(t *testing.T) {
	h := templateHash(nil)
	base := shopAPIBase(true, h)
	base.Nodes[1].Attributes["maxUnavailable"] = "1"
	base.Nodes[1].Attributes["maxSurge"] = "1"
	newHash := templateHash(func(tpl map[string]any) {
		tpl["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)["env"] = []any{
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
				"podTemplateHash": newHash, "podTemplateChanged": true,
			},
		}},
	}
	v := impact.PDBRollout{}.Evaluate(pdbCtx(base, ch))
	if v.Status != impact.Pass {
		t.Fatalf("maxUnavailable:1 with minAvailable=2 want PASS, got %s: %s evidence=%v",
			v.Status, v.Explanation, v.Evidence)
	}
}

func TestPDBRollout_Default25PercentOf3_DoesNotFalseBlock(t *testing.T) {
	h := templateHash(nil)
	newHash := templateHash(func(tpl map[string]any) {
		tpl["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)["env"] = []any{
			map[string]any{"name": "FEATURE_FLAGS", "value": "on"},
		}
	})
	base := &graph.Snapshot{
		ID: "default-mu", Phase: graph.PhaseBaseline,
		Meta: map[string]any{"pdbsCollected": true},
		Nodes: []graph.Node{
			{ID: "workload/shop/api", Kind: graph.KindWorkload, Name: "api", Namespace: "shop",
				Attributes: map[string]any{
					"replicas": 3, "type": "Deployment",
					"podTemplateHash": h,
					"podLabels":       map[string]any{"app": "api"},
				}},
			{ID: "pdb/shop/api", Kind: graph.KindPDB, Name: "api", Namespace: "shop",
				Attributes: map[string]any{"minAvailable": 2, "matchLabels": map[string]any{"app": "api"}}},
		},
		Edges: []graph.Edge{{From: "workload/shop/api", To: "pdb/shop/api", Rel: graph.RelProtectedBy}},
	}
	ch := &loader.ChangeEnvelope{
		Kind: "argo-sync", ID: "default", Facts: map[string]any{"scenario": "argo-impact"},
		Seeds: []string{"workload/shop/api"},
		PatchNodes: []graph.Node{{
			ID: "workload/shop/api",
			Attributes: map[string]any{
				"replicas": 3, "podTemplateHash": newHash, "podTemplateChanged": true,
			},
		}},
	}
	v := impact.PDBRollout{}.Evaluate(pdbCtx(base, ch))
	if v.Status != impact.Pass {
		t.Fatalf("default 25pct of 3 (=0 unavailable) want PASS, got %s: %s evidence=%v",
			v.Status, v.Explanation, v.Evidence)
	}
}
