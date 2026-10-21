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

func TestPDBRollout_ZeroAvailableWithoutPDB_Blocks(t *testing.T) {
	base := &graph.Snapshot{
		ID: "fp4", Phase: graph.PhaseBaseline,
		Meta: map[string]any{"pdbsCollected": true, "pod_scheduling_capacity_estimate": 50},
		Nodes: []graph.Node{
			{ID: "cluster/c", Kind: graph.KindCluster, Name: "c"},
			{ID: "workload/shop/api", Kind: graph.KindWorkload, Name: "api", Namespace: "shop",
				Attributes: map[string]any{"replicas": 3, "type": "Deployment", "podLabels": map[string]any{"app": "api"}}},
		},
	}
	ch := &loader.ChangeEnvelope{
		Kind: "argo-sync", ID: "zero-no-pdb", Facts: map[string]any{"scenario": "argo-impact"},
		Seeds: []string{"workload/shop/api"},
		PatchNodes: []graph.Node{{
			ID: "workload/shop/api",
			Attributes: map[string]any{
				"replicas":       3,
				"maxUnavailable": "100%",
				"maxSurge":       "0",
			},
		}},
	}
	v := impact.PDBRollout{}.Evaluate(pdbCtx(base, ch))
	if v.Status != impact.Block {
		t.Fatalf("maxUnavailable 100pct without PDB want BLOCK, got %s: %s", v.Status, v.Explanation)
	}
	if !strings.Contains(v.Explanation, "do not limit Deployment rollouts") &&
		!strings.Contains(strings.Join(v.Evidence, " "), "take available pods to 0") {
		t.Fatalf("want plain wording that PDB does not limit rollouts, got explanation=%q evidence=%v",
			v.Explanation, v.Evidence)
	}
}

func TestPDBRollout_ZeroAvailableWithPDB_BlocksAndNotesPDB(t *testing.T) {
	base := &graph.Snapshot{
		ID: "c5", Phase: graph.PhaseBaseline,
		Meta: map[string]any{"pdbsCollected": true, "pod_scheduling_capacity_estimate": 50},
		Nodes: []graph.Node{
			{ID: "cluster/c", Kind: graph.KindCluster, Name: "c"},
			{ID: "workload/shop/api", Kind: graph.KindWorkload, Name: "api", Namespace: "shop",
				Attributes: map[string]any{"replicas": 3, "type": "Deployment", "podLabels": map[string]any{"app": "api"}}},
			{ID: "pdb/shop/api", Kind: graph.KindPDB, Name: "api", Namespace: "shop",
				Attributes: map[string]any{"minAvailable": 2, "matchLabels": map[string]any{"app": "api"}}},
		},
		Edges: []graph.Edge{
			{From: "workload/shop/api", To: "pdb/shop/api", Rel: graph.RelProtectedBy},
		},
	}
	ch := &loader.ChangeEnvelope{
		Kind: "argo-sync", ID: "c5", Facts: map[string]any{"scenario": "argo-impact"},
		Seeds: []string{"workload/shop/api"},
		PatchNodes: []graph.Node{{
			ID: "workload/shop/api",
			Attributes: map[string]any{
				"replicas":       3,
				"maxUnavailable": "100%",
				"maxSurge":       "0",
			},
		}},
	}
	v := impact.PDBRollout{}.Evaluate(pdbCtx(base, ch))
	if v.Status != impact.Block {
		t.Fatalf("C5 maxUnavailable 100pct with PDB want BLOCK, got %s: %s", v.Status, v.Explanation)
	}
	blob := v.Explanation + " " + strings.Join(v.Evidence, " ")
	if !strings.Contains(blob, "take available pods to 0") {
		t.Fatalf("want zero-available wording, got %q", blob)
	}
	if !strings.Contains(blob, "does not protect against rollouts") &&
		!strings.Contains(blob, "do not protect against rollouts") {
		t.Fatalf("want explicit note that PDB does not protect rollouts, got %q", blob)
	}
	if !strings.Contains(blob, "minAvailable=2") {
		t.Fatalf("want minAvailable=2 in message, got %q", blob)
	}
}

func TestPDBRollout_MaxUnavailableOne_Passes(t *testing.T) {
	base := &graph.Snapshot{
		ID: "fp3", Phase: graph.PhaseBaseline,
		Meta: map[string]any{"pdbsCollected": true, "pod_scheduling_capacity_estimate": 50},
		Nodes: []graph.Node{
			{ID: "cluster/c", Kind: graph.KindCluster, Name: "c"},
			{ID: "workload/shop/api", Kind: graph.KindWorkload, Name: "api", Namespace: "shop",
				Attributes: map[string]any{"replicas": 3, "type": "Deployment", "podLabels": map[string]any{"app": "api"}}},
			{ID: "pdb/shop/api", Kind: graph.KindPDB, Name: "api", Namespace: "shop",
				Attributes: map[string]any{"minAvailable": 2, "matchLabels": map[string]any{"app": "api"}}},
		},
		Edges: []graph.Edge{
			{From: "workload/shop/api", To: "pdb/shop/api", Rel: graph.RelProtectedBy},
		},
	}
	ch := &loader.ChangeEnvelope{
		Kind: "argo-sync", ID: "fp3", Facts: map[string]any{"scenario": "argo-impact"},
		Seeds: []string{"workload/shop/api"},
		PatchNodes: []graph.Node{{
			ID: "workload/shop/api",
			Attributes: map[string]any{
				"replicas":       3,
				"maxUnavailable": "1",
				"maxSurge":       "1",
			},
		}},
	}
	v := impact.PDBRollout{}.Evaluate(pdbCtx(base, ch))
	if v.Status != impact.Pass {
		t.Fatalf("maxUnavailable:1 with minAvailable=2 want PASS, got %s: %s evidence=%v",
			v.Status, v.Explanation, v.Evidence)
	}
}

func TestPDBRollout_MaxUnavailableGteReplicas_Blocks(t *testing.T) {
	base := &graph.Snapshot{
		ID: "mu-ge-rep", Phase: graph.PhaseBaseline,
		Meta: map[string]any{"pdbsCollected": true},
		Nodes: []graph.Node{
			{ID: "workload/shop/api", Kind: graph.KindWorkload, Name: "api", Namespace: "shop",
				Attributes: map[string]any{"replicas": 3, "type": "Deployment"}},
		},
	}
	ch := &loader.ChangeEnvelope{
		Kind: "argo-sync", ID: "mu3", Facts: map[string]any{"scenario": "argo-impact"},
		Seeds: []string{"workload/shop/api"},
		PatchNodes: []graph.Node{{
			ID: "workload/shop/api",
			Attributes: map[string]any{"replicas": 3, "maxUnavailable": "3", "maxSurge": "0"},
		}},
	}
	v := impact.PDBRollout{}.Evaluate(pdbCtx(base, ch))
	if v.Status != impact.Block {
		t.Fatalf("maxUnavailable>=replicas want BLOCK, got %s: %s", v.Status, v.Explanation)
	}
}

func TestPDBRollout_Default25PercentOf3_DoesNotFalseBlock(t *testing.T) {
	// Default maxUnavailable is floor(25%*3)=0, so remaining=3 >= minAvailable=2.
	base := &graph.Snapshot{
		ID: "default-mu", Phase: graph.PhaseBaseline,
		Meta: map[string]any{"pdbsCollected": true},
		Nodes: []graph.Node{
			{ID: "workload/shop/api", Kind: graph.KindWorkload, Name: "api", Namespace: "shop",
				Attributes: map[string]any{"replicas": 3, "type": "Deployment", "podLabels": map[string]any{"app": "api"}}},
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
			Attributes: map[string]any{"replicas": 3}, // no maxUnavailable → default 25%
		}},
	}
	v := impact.PDBRollout{}.Evaluate(pdbCtx(base, ch))
	if v.Status != impact.Pass {
		t.Fatalf("default 25%% of 3 (=0 unavailable) want PASS, got %s: %s evidence=%v",
			v.Status, v.Explanation, v.Evidence)
	}
}
