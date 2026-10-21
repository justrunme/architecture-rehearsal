package impact_test

import (
	"strings"
	"testing"

	"github.com/justrunme/architecture-rehearsal/internal/graph"
	"github.com/justrunme/architecture-rehearsal/internal/impact"
	"github.com/justrunme/architecture-rehearsal/internal/loader"
)

func pdbCtx(base *graph.Snapshot, ch *loader.ChangeEnvelope) impact.Context {
	prop := loader.ApplyChange(base, ch)
	return impact.Context{
		Baseline: base, Proposed: prop, Change: ch,
		BaseIdx: graph.BuildIndex(base), PropIdx: graph.BuildIndex(prop),
		PDBsCollected: true,
	}
}

func shopAPIBase(withPDB bool, images, env string) *graph.Snapshot {
	nodes := []graph.Node{
		{ID: "cluster/c", Kind: graph.KindCluster, Name: "c"},
		{ID: "workload/shop/api", Kind: graph.KindWorkload, Name: "api", Namespace: "shop",
			Attributes: map[string]any{
				"replicas": 3, "type": "Deployment",
				"maxUnavailable": "100%", "maxSurge": "0",
				"podLabels":      map[string]any{"app": "api"},
				"containerImages": images,
				"containerEnv":    env,
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
	base := shopAPIBase(true, "api=nginx:1.27", "")
	// No-op sync: strategy already dangerous, template identical.
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

func TestPDBRollout_EnvChange_Blocks(t *testing.T) {
	base := shopAPIBase(true, "api=nginx:1.27", "")
	ch := &loader.ChangeEnvelope{
		Kind: "argo-sync", ID: "env", Facts: map[string]any{"scenario": "argo-impact"},
		Seeds: []string{"workload/shop/api"},
		PatchNodes: []graph.Node{{
			ID: "workload/shop/api",
			Attributes: map[string]any{
				"replicas":            3,
				"maxUnavailable":      "100%",
				"maxSurge":            "0",
				"containerImages":     "api=nginx:1.27",
				"containerEnv":        "api:FEATURE_FLAGS=fast-checkout=on",
				"podTemplateChanged":  true,
			},
		}},
	}
	v := impact.PDBRollout{}.Evaluate(pdbCtx(base, ch))
	if v.Status != impact.Block {
		t.Fatalf("env template change want BLOCK, got %s: %s", v.Status, v.Explanation)
	}
	blob := strings.Join(v.Evidence, " ")
	if !strings.Contains(blob, "take available pods to 0") {
		t.Fatalf("want zero-available BLOCK evidence, got %v", v.Evidence)
	}
}

func TestPDBRollout_ImageChange_Blocks(t *testing.T) {
	base := shopAPIBase(true, "api=nginx:1.27", "")
	ch := &loader.ChangeEnvelope{
		Kind: "argo-sync", ID: "image", Facts: map[string]any{"scenario": "argo-impact"},
		Seeds: []string{"workload/shop/api"},
		PatchNodes: []graph.Node{{
			ID: "workload/shop/api",
			Attributes: map[string]any{
				"replicas":           3,
				"maxUnavailable":     "100%",
				"maxSurge":           "0",
				"containerImages":    "api=nginx:1.28",
				"podTemplateChanged": true,
			},
		}},
	}
	v := impact.PDBRollout{}.Evaluate(pdbCtx(base, ch))
	if v.Status != impact.Block {
		t.Fatalf("image template change want BLOCK, got %s: %s", v.Status, v.Explanation)
	}
}

func TestPDBRollout_ZeroAvailableWithoutPDB_EnvBlocks(t *testing.T) {
	base := shopAPIBase(false, "api=nginx:1.27", "")
	// Clear dangerous strategy on baseline; introduce via patch with env change.
	base.Nodes[1].Attributes["maxUnavailable"] = "1"
	ch := &loader.ChangeEnvelope{
		Kind: "argo-sync", ID: "zero-no-pdb", Facts: map[string]any{"scenario": "argo-impact"},
		Seeds: []string{"workload/shop/api"},
		PatchNodes: []graph.Node{{
			ID: "workload/shop/api",
			Attributes: map[string]any{
				"replicas":           3,
				"maxUnavailable":     "100%",
				"maxSurge":           "0",
				"containerImages":    "api=nginx:1.27",
				"containerEnv":       "api:FEATURE_FLAGS=on",
				"podTemplateChanged": true,
			},
		}},
	}
	v := impact.PDBRollout{}.Evaluate(pdbCtx(base, ch))
	if v.Status != impact.Block {
		t.Fatalf("maxUnavailable 100pct without PDB + env want BLOCK, got %s: %s", v.Status, v.Explanation)
	}
}

func TestPDBRollout_BelowMinAvailable_BlocksWithWording(t *testing.T) {
	// 99% of 3 → floor 2 unavailable → 1 left, below minAvailable=2.
	base := &graph.Snapshot{
		ID: "x2", Phase: graph.PhaseBaseline,
		Meta: map[string]any{"pdbsCollected": true},
		Nodes: []graph.Node{
			{ID: "workload/shop/api", Kind: graph.KindWorkload, Name: "api", Namespace: "shop",
				Attributes: map[string]any{
					"replicas": 3, "type": "Deployment",
					"containerImages": "api=nginx:1.27",
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
				"replicas":           3,
				"maxUnavailable":     "99%",
				"containerImages":    "api=nginx:1.27",
				"containerEnv":       "api:FEATURE_FLAGS=on",
				"podTemplateChanged": true,
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
	base := shopAPIBase(true, "api=nginx:1.27", "")
	base.Nodes[1].Attributes["maxUnavailable"] = "1"
	base.Nodes[1].Attributes["maxSurge"] = "1"
	ch := &loader.ChangeEnvelope{
		Kind: "argo-sync", ID: "fp3", Facts: map[string]any{"scenario": "argo-impact"},
		Seeds: []string{"workload/shop/api"},
		PatchNodes: []graph.Node{{
			ID: "workload/shop/api",
			Attributes: map[string]any{
				"replicas":           3,
				"maxUnavailable":     "1",
				"maxSurge":           "1",
				"containerImages":    "api=nginx:1.27",
				"containerEnv":       "api:FEATURE_FLAGS=on",
				"podTemplateChanged": true,
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
	base := &graph.Snapshot{
		ID: "default-mu", Phase: graph.PhaseBaseline,
		Meta: map[string]any{"pdbsCollected": true},
		Nodes: []graph.Node{
			{ID: "workload/shop/api", Kind: graph.KindWorkload, Name: "api", Namespace: "shop",
				Attributes: map[string]any{
					"replicas": 3, "type": "Deployment",
					"containerImages": "api=nginx:1.27",
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
				"replicas":           3,
				"containerImages":    "api=nginx:1.27",
				"containerEnv":       "api:FEATURE_FLAGS=on",
				"podTemplateChanged": true,
			},
		}},
	}
	v := impact.PDBRollout{}.Evaluate(pdbCtx(base, ch))
	if v.Status != impact.Pass {
		t.Fatalf("default 25pct of 3 (=0 unavailable) want PASS, got %s: %s evidence=%v",
			v.Status, v.Explanation, v.Evidence)
	}
}
