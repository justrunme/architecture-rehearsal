package impact_test

import (
	"path/filepath"
	"testing"

	"github.com/justrunme/architecture-rehearsal/internal/argo"
	"github.com/justrunme/architecture-rehearsal/internal/change"
	"github.com/justrunme/architecture-rehearsal/internal/graph"
	"github.com/justrunme/architecture-rehearsal/internal/impact"
	"github.com/justrunme/architecture-rehearsal/internal/loader"
)

func loadArgo(t *testing.T, scenario string) (*graph.Snapshot, *loader.ChangeEnvelope) {
	t.Helper()
	root := filepath.Join("..", "..", "examples", "e2e-argo", scenario)
	base, err := loader.LoadSnapshot(filepath.Join(root, "baseline.json"))
	if err != nil {
		t.Fatal(err)
	}
	ch, err := argo.Compile(base, argo.CompileOptions{
		AppName:    "demo-" + scenario,
		DesiredDir: filepath.Join(root, "desired"),
		ChangeID:   "test-" + scenario,
		Scope:      change.ManifestScope{Namespaces: []string{"demo"}},
		Source:     argo.SourceDirectory,
	})
	if err != nil {
		t.Fatal(err)
	}
	return base, ch
}

func TestSchedulingCapacity_UnschedulableBlock(t *testing.T) {
	base, ch := loadArgo(t, "unschedulable")
	rep := impact.Run(base, ch, []impact.Detector{impact.SchedulingCapacity{}})
	v := rep.Detectors[0]
	if v.Status != impact.Block {
		t.Fatalf("expected BLOCK, got %s: %s", v.Status, v.Explanation)
	}
}

func TestNetworkPolicyCut_Block(t *testing.T) {
	base, ch := loadArgo(t, "netpol-cut")
	rep := impact.Run(base, ch, []impact.Detector{impact.NetworkPolicyCut{}})
	v := rep.Detectors[0]
	if v.Status != impact.Block {
		t.Fatalf("expected BLOCK, got %s: %s (change seeds=%v added=%d)", v.Status, v.Explanation, ch.Seeds, len(ch.AddedNodes))
	}
}

func TestPDBRollout_WarnOrBlock(t *testing.T) {
	base, ch := loadArgo(t, "pdb-violation")
	rep := impact.Run(base, ch, []impact.Detector{impact.PDBRollout{}})
	v := rep.Detectors[0]
	if v.Status != impact.Block && v.Status != impact.Warn {
		t.Fatalf("expected WARN or BLOCK, got %s: %s", v.Status, v.Explanation)
	}
}

func TestBenign_Pass(t *testing.T) {
	base, ch := loadArgo(t, "benign")
	rep := impact.Run(base, ch, nil)
	if rep.Verdict != impact.Pass {
		t.Fatalf("expected PASS gate, got %s: %+v", rep.Verdict, rep.Detectors)
	}
}

func TestScheduling_UnknownWithoutCapacity(t *testing.T) {
	base, ch := loadArgo(t, "unschedulable")
	delete(base.Meta, "pod_scheduling_capacity_estimate")
	for i := range base.Nodes {
		if base.Nodes[i].Kind == graph.KindNode {
			delete(base.Nodes[i].Attributes, "allocatablePods")
		}
	}
	rep := impact.Run(base, ch, []impact.Detector{impact.SchedulingCapacity{}})
	if rep.Detectors[0].Status != impact.Unknown {
		t.Fatalf("expected UNKNOWN when capacity missing, got %s", rep.Detectors[0].Status)
	}
}

func TestPVC_ZoneConflictBlock(t *testing.T) {
	base := &graph.Snapshot{
		ID: "pvc-base", Phase: graph.PhaseBaseline,
		Nodes: []graph.Node{
			{ID: "cluster/c", Kind: graph.KindCluster, Name: "c"},
			{ID: "node/n1", Kind: graph.KindNode, Name: "n1", Attributes: map[string]any{"zone": "z2", "allocatablePods": 10}},
			{ID: "workload/demo/api", Kind: graph.KindWorkload, Name: "api", Namespace: "demo",
				Attributes: map[string]any{"replicas": 1, "nodeSelector": map[string]any{"topology.kubernetes.io/zone": "z2"}}},
			{ID: "pvc/demo/data", Kind: graph.KindPVC, Name: "data", Namespace: "demo",
				Attributes: map[string]any{"zone": "z1", "storageClass": "zonal-ssd"}},
		},
		Edges: []graph.Edge{
			{From: "workload/demo/api", To: "pvc/demo/data", Rel: graph.RelBindsVolume},
		},
		Meta: map[string]any{"pod_scheduling_capacity_estimate": 10},
	}
	ch := &loader.ChangeEnvelope{
		Kind: "argo-sync", ID: "pvc-conflict", Title: "pvc",
		Facts: map[string]any{"scenario": "argo-impact"},
		Seeds: []string{"pvc/demo/data"},
		PatchNodes: []graph.Node{
			{ID: "pvc/demo/data", Attributes: map[string]any{"zone": "z1", "storageClass": "zonal-ssd"}},
		},
	}
	v := impact.PVCZoneConflict{}.Evaluate(impact.Context{
		Baseline: base, Proposed: loader.ApplyChange(base, ch), Change: ch,
		BaseIdx: graph.BuildIndex(base), PropIdx: graph.BuildIndex(loader.ApplyChange(base, ch)),
	})
	if v.Status != impact.Block {
		t.Fatalf("expected BLOCK, got %s: %s", v.Status, v.Explanation)
	}
}

func TestPVC_UnknownMissingNodeZones(t *testing.T) {
	base := &graph.Snapshot{
		ID: "pvc-unk", Phase: graph.PhaseBaseline,
		Nodes: []graph.Node{
			{ID: "cluster/c", Kind: graph.KindCluster, Name: "c"},
			{ID: "pvc/demo/data", Kind: graph.KindPVC, Name: "data", Namespace: "demo",
				Attributes: map[string]any{"zone": "z1"}},
		},
	}
	ch := &loader.ChangeEnvelope{
		Kind: "argo-sync", ID: "x", Facts: map[string]any{"scenario": "argo-impact"},
		Seeds: []string{"pvc/demo/data"},
	}
	v := impact.PVCZoneConflict{}.Evaluate(impact.Context{
		Baseline: base, Proposed: loader.ApplyChange(base, ch), Change: ch,
		BaseIdx: graph.BuildIndex(base), PropIdx: graph.BuildIndex(loader.ApplyChange(base, ch)),
	})
	if v.Status != impact.Unknown {
		t.Fatalf("expected UNKNOWN, got %s: %s", v.Status, v.Explanation)
	}
}

func TestBlastRadius_Warn(t *testing.T) {
	base, ch := loadArgo(t, "unschedulable")
	// Ensure seeds present from scale
	rep := impact.Run(base, ch, []impact.Detector{impact.BlastRadius{}})
	// api→postgres dependency: at least WARN or PASS depending on fan-out
	if rep.Detectors[0].Status == impact.Unknown {
		t.Fatalf("unexpected UNKNOWN: %s", rep.Detectors[0].Explanation)
	}
}

func TestGate_UnschedulableEndToEnd(t *testing.T) {
	base, ch := loadArgo(t, "unschedulable")
	rep := impact.Run(base, ch, nil)
	if rep.Verdict != impact.Block {
		t.Fatalf("gate want BLOCK got %s\n%s", rep.Verdict, impact.Markdown(rep))
	}
	md := impact.Markdown(rep)
	if md == "" {
		t.Fatal("empty markdown")
	}
}

func TestNetpol_UnknownWithoutDeps(t *testing.T) {
	base, ch := loadArgo(t, "netpol-cut")
	base.Edges = nil
	prop := loader.ApplyChange(base, ch)
	v := impact.NetworkPolicyCut{}.Evaluate(impact.Context{
		Baseline: base, Proposed: prop, Change: ch,
		BaseIdx: graph.BuildIndex(base), PropIdx: graph.BuildIndex(prop),
	})
	if v.Status != impact.Unknown {
		t.Fatalf("expected UNKNOWN without DEPENDS_ON, got %s", v.Status)
	}
}
