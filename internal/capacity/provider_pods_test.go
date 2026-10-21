package capacity_test

import (
	"fmt"
	"testing"

	"github.com/justrunme/architecture-rehearsal/internal/capacity"
	"github.com/justrunme/architecture-rehearsal/internal/graph"
)

func TestSchedulingEstimate_PrefersClusterPodsOverWorkloads(t *testing.T) {
	snap := &graph.Snapshot{
		Meta: map[string]any{
			"pod_scheduling_capacity_estimate": 106,
		},
		Nodes: []graph.Node{
			{ID: "node/n1", Kind: graph.KindNode, Attributes: map[string]any{"allocatablePods": 110}},
			{ID: "workload/shop/api", Kind: graph.KindWorkload, Attributes: map[string]any{"replicas": 3}},
		},
	}
	for i := 0; i < 106; i++ {
		snap.Nodes = append(snap.Nodes, graph.Node{
			ID:         fmt.Sprintf("pod/other/f-%d", i),
			Kind:       graph.KindPod,
			Attributes: map[string]any{"phase": "Running"},
		})
	}
	r, err := (capacity.SchedulingEstimate{}).Read(snap)
	if err != nil {
		t.Fatal(err)
	}
	if r.Available != 4 {
		t.Fatalf("want available=4 (110-106), got %d detail=%s", r.Available, r.Detail)
	}
}

func TestSchedulingEstimate_MissingPodRequestsCountAsZero(t *testing.T) {
	// kube-proxy-like pods without requests must not disable CPU accounting.
	snap := &graph.Snapshot{
		Nodes: []graph.Node{
			{ID: "node/n1", Kind: graph.KindNode, Attributes: map[string]any{
				"allocatablePods":       110,
				"allocatableCPUMillis":  8000,
				"allocatableMemoryBytes": 8 * 1024 * 1024 * 1024,
			}},
			{ID: "pod/kube-system/kube-proxy", Kind: graph.KindPod, Attributes: map[string]any{
				"phase": "Running", // no cpuRequestMillis
			}},
			{ID: "pod/other/hog", Kind: graph.KindPod, Attributes: map[string]any{
				"phase":            "Running",
				"cpuRequestMillis": 6900,
			}},
			{ID: "pod/shop/api-1", Kind: graph.KindPod, Attributes: map[string]any{
				"phase":            "Running",
				"cpuRequestMillis": 150,
			}},
			{ID: "pod/shop/api-2", Kind: graph.KindPod, Attributes: map[string]any{
				"phase":            "Running",
				"cpuRequestMillis": 150,
			}},
			{ID: "pod/shop/api-3", Kind: graph.KindPod, Attributes: map[string]any{
				"phase":            "Running",
				"cpuRequestMillis": 150,
			}},
		},
	}
	r, err := (capacity.SchedulingEstimate{}).Read(snap)
	if err != nil {
		t.Fatal(err)
	}
	if !r.HaveCPU {
		t.Fatal("expected HaveCPU")
	}
	// 8000 - 6900 - 150*3 = 650 (kube-proxy requests count as 0)
	if r.AvailableCPUMillis != 650 {
		t.Fatalf("want availableCPU=650, got %d detail=%s", r.AvailableCPUMillis, r.Detail)
	}
}
