package capacity_test

import (
	"testing"

	"github.com/justrunme/architecture-rehearsal/internal/capacity"
	"github.com/justrunme/architecture-rehearsal/internal/graph"
)

func TestSchedulingVsCNI(t *testing.T) {
	snap := &graph.Snapshot{
		Meta: map[string]any{"pod_scheduling_capacity_estimate": 12},
		Nodes: []graph.Node{
			{ID: "node/n1", Kind: graph.KindNode, Attributes: map[string]any{"allocatablePods": 20}},
		},
	}
	r, err := (capacity.SchedulingEstimate{}).Read(snap)
	if err != nil || r.Available != 12 || r.Source != capacity.SourceSchedulingEstimate {
		t.Fatalf("%+v err=%v", r, err)
	}
	_, err = (capacity.CNIExplicit{}).Read(snap)
	if err == nil {
		t.Fatal("expected missing cni meta error")
	}
	snap.Meta["cni_ip_available"] = 7
	r, err = (capacity.Best{}).Read(snap)
	if err != nil || r.Available != 7 || r.Source != capacity.SourceCNIExplicit {
		t.Fatalf("%+v err=%v", r, err)
	}
}
