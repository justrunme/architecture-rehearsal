package scenario_test

import (
	"path/filepath"
	"testing"

	"github.com/justrunme/architecture-rehearsal/internal/graph"
	"github.com/justrunme/architecture-rehearsal/internal/loader"
	"github.com/justrunme/architecture-rehearsal/internal/scenario"
)

func ctxFrom(t *testing.T, dir string) scenario.Context {
	t.Helper()
	base, err := loader.LoadSnapshot(filepath.Join(dir, "baseline.json"))
	if err != nil {
		t.Fatal(err)
	}
	ch, err := loader.LoadChange(filepath.Join(dir, "change.json"))
	if err != nil {
		t.Fatal(err)
	}
	prop := loader.ApplyChange(base, ch)
	return scenario.Context{
		Baseline: base, Proposed: prop, Change: ch,
		BaseIdx: graph.BuildIndex(base), PropIdx: graph.BuildIndex(prop),
	}
}

func TestRWO_PositiveNegative(t *testing.T) {
	ctx := ctxFrom(t, "../../examples/golden/rwo-node-loss")
	r := scenario.RWONodeLoss{}
	res := r.Evaluate(ctx)
	if res.Outcome != scenario.OutcomeMatched || len(res.Findings) == 0 {
		t.Fatalf("positive: %+v", res)
	}
	// RWX negative
	for i := range ctx.Baseline.Nodes {
		if ctx.Baseline.Nodes[i].Kind == graph.KindPVC {
			ctx.Baseline.Nodes[i].Attributes["accessMode"] = "ReadWriteMany"
		}
	}
	ctx.BaseIdx = graph.BuildIndex(ctx.Baseline)
	res2 := r.Evaluate(ctx)
	if res2.Outcome != scenario.OutcomeNotMatched {
		t.Fatalf("RWX should not match: %+v", res2)
	}
}

func TestCNI_PositiveNegative(t *testing.T) {
	ctx := ctxFrom(t, "../../examples/golden/cni-ip-capacity")
	r := scenario.CNICapacity{}
	if miss := r.MissingRequirements(ctx); len(miss) != 0 {
		t.Fatalf("missing: %v", miss)
	}
	res := r.Evaluate(ctx)
	if res.Outcome != scenario.OutcomeMatched {
		t.Fatalf("positive: %+v", res)
	}
	ctx.Baseline.Meta["pod_ip_capacity_available"] = 10000
	// shrink proposed back
	for i := range ctx.Proposed.Nodes {
		if ctx.Proposed.Nodes[i].ID == "workload/payments/api" {
			ctx.Proposed.Nodes[i].Attributes["replicas"] = 6
		}
		if ctx.Proposed.Nodes[i].ID == "workload/payments/worker" {
			ctx.Proposed.Nodes[i].Attributes["replicas"] = 4
		}
	}
	ctx.PropIdx = graph.BuildIndex(ctx.Proposed)
	res2 := r.Evaluate(ctx)
	if res2.Outcome != scenario.OutcomeNotMatched {
		t.Fatalf("ample capacity should not match: %+v", res2)
	}
}

func TestProm_PositiveNegative(t *testing.T) {
	ctx := ctxFrom(t, "../../examples/golden/prom-zero-match")
	r := scenario.PromZeroMatch{}
	res := r.Evaluate(ctx)
	if res.Outcome != scenario.OutcomeMatched {
		t.Fatalf("positive: %+v", res)
	}
	ctx.Change.Facts["label_matchers"] = map[string]any{"namespace": "gitops"}
	ctx.Change.Facts["metric"] = "kube_pod_status_ready"
	res2 := r.Evaluate(ctx)
	if res2.Outcome != scenario.OutcomeNotMatched {
		t.Fatalf("valid selector: %+v", res2)
	}
}

func TestCNI_UnknownWithoutCapacity(t *testing.T) {
	ctx := ctxFrom(t, "../../examples/golden/cni-ip-capacity")
	delete(ctx.Baseline.Meta, "pod_ip_capacity_available")
	// strip node capacity
	for i := range ctx.Baseline.Nodes {
		if ctx.Baseline.Nodes[i].Kind == graph.KindNode {
			delete(ctx.Baseline.Nodes[i].Attributes, "allocatablePods")
		}
	}
	ctx.BaseIdx = graph.BuildIndex(ctx.Baseline)
	r := scenario.CNICapacity{}
	if !r.Applicable(ctx) {
		t.Fatal("should be applicable")
	}
	miss := r.MissingRequirements(ctx)
	if len(miss) == 0 {
		t.Fatal("expected missing capacity requirement")
	}
}

func TestServiceRouting_RequiresEdges(t *testing.T) {
	ctx := ctxFrom(t, "../../examples/golden/rwo-node-loss")
	// remove ROUTES_TO edges
	var edges []graph.Edge
	for _, e := range ctx.Baseline.Edges {
		if e.Rel != graph.RelRoutesTo {
			edges = append(edges, e)
		}
	}
	ctx.Baseline.Edges = edges
	ctx.BaseIdx = graph.BuildIndex(ctx.Baseline)
	r := scenario.ServiceRouting{}
	// add a service removal change that would need edges
	ctx.Change.RemovedNodes = []string{"workload/gitops/gitaly"}
	miss := r.MissingRequirements(ctx)
	// may or may not have service - rwo fixture has ROUTES_TO from svc
	// after remove all ROUTES_TO, should require edges if services exist
	if hasService(ctx) && len(miss) == 0 {
		t.Fatal("expected service-edges requirement")
	}
}

func hasService(ctx scenario.Context) bool {
	for _, n := range ctx.Baseline.Nodes {
		if n.Kind == graph.KindService {
			return true
		}
	}
	return false
}
