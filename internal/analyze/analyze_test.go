package analyze_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/justrunme/architecture-rehearsal/internal/analyze"
	"github.com/justrunme/architecture-rehearsal/internal/loader"
	"github.com/justrunme/architecture-rehearsal/internal/scenario"
)

func golden(t *testing.T, name string) (string, string) {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Join(filepath.Dir(file), "..", "..", "examples", "golden", name)
	return filepath.Join(root, "baseline.json"), filepath.Join(root, "change.json")
}

func TestGoldenRWO(t *testing.T) {
	b, c := golden(t, "rwo-node-loss")
	base, err := loader.LoadSnapshot(b)
	if err != nil {
		t.Fatal(err)
	}
	ch, err := loader.LoadChange(c)
	if err != nil {
		t.Fatal(err)
	}
	// snapshot baseline attrs before apply
	rawBefore, _ := json.Marshal(base.Nodes)
	rep, err := analyze.Run(base, ch)
	if err != nil {
		t.Fatal(err)
	}
	rawAfter, _ := json.Marshal(base.Nodes)
	if string(rawBefore) != string(rawAfter) {
		t.Fatal("baseline mutated by analyze.Run / ApplyChange")
	}
	if rep.Decision != analyze.DecisionBlock {
		t.Fatalf("decision=%s risk=%s", rep.Decision, rep.Risk)
	}
}

func TestGoldenCNIDerivedFromReplicas(t *testing.T) {
	b, c := golden(t, "cni-ip-capacity")
	base, _ := loader.LoadSnapshot(b)
	ch, _ := loader.LoadChange(c)
	// ensure facts do NOT pre-supply pods_requested
	delete(ch.Facts, "pods_requested")
	delete(ch.Facts, "pod_ip_capacity_available")
	// capacity stays in baseline meta
	rep, err := analyze.Run(base, ch)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, f := range rep.Findings {
		if f.Scenario == "cni-ip-capacity" {
			found = true
			if f.Risk != "critical" && f.Risk != "high" {
				t.Fatalf("risk=%s", f.Risk)
			}
		}
	}
	if !found {
		t.Fatalf("expected derived cni finding: %+v", rep.Findings)
	}
}

func TestGoldenPromMetricSpecific(t *testing.T) {
	b, c := golden(t, "prom-zero-match")
	base, _ := loader.LoadSnapshot(b)
	ch, _ := loader.LoadChange(c)
	rep, err := analyze.Run(base, ch)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Findings) == 0 || rep.Findings[0].Scenario != "prom-zero-match" {
		t.Fatalf("%+v", rep.Findings)
	}
}

func TestBaselineImmutableAfterPatch(t *testing.T) {
	b, c := golden(t, "cni-ip-capacity")
	base, _ := loader.LoadSnapshot(b)
	ch, _ := loader.LoadChange(c)
	// record original replica
	var orig int
	for _, n := range base.Nodes {
		if n.ID == "workload/payments/api" {
			orig = n.AttrInt("replicas")
		}
	}
	_ = loader.ApplyChange(base, ch)
	for _, n := range base.Nodes {
		if n.ID == "workload/payments/api" {
			if n.AttrInt("replicas") != orig {
				t.Fatalf("baseline replicas mutated: got %d want %d", n.AttrInt("replicas"), orig)
			}
		}
	}
}

func TestCNISufficientCapacityNoFinding(t *testing.T) {
	b, c := golden(t, "cni-ip-capacity")
	base, _ := loader.LoadSnapshot(b)
	ch, _ := loader.LoadChange(c)
	base.Meta["pod_ip_capacity_available"] = 1000
	// small scale
	ch.PatchNodes = ch.PatchNodes[:1]
	ch.PatchNodes[0].Attributes["replicas"] = 7
	rep, err := analyze.Run(base, ch)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range rep.Findings {
		if f.Scenario == "cni-ip-capacity" {
			t.Fatalf("unexpected cni finding: %+v", f)
		}
	}
}

func TestRWXNoRWOFinding(t *testing.T) {
	b, c := golden(t, "rwo-node-loss")
	base, _ := loader.LoadSnapshot(b)
	ch, _ := loader.LoadChange(c)
	for i := range base.Nodes {
		if base.Nodes[i].Kind == "PVC" {
			base.Nodes[i].Attributes["accessMode"] = "ReadWriteMany"
		}
	}
	rep, err := analyze.Run(base, ch)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range rep.Findings {
		if f.Scenario == "rwo-node-loss" {
			t.Fatal("RWX should not trigger RWO scenario")
		}
	}
}

func TestPromSelectorExistsNoFinding(t *testing.T) {
	b, c := golden(t, "prom-zero-match")
	base, _ := loader.LoadSnapshot(b)
	ch, _ := loader.LoadChange(c)
	ch.Facts["label_matchers"] = map[string]any{"namespace": "gitops"}
	ch.Facts["expr"] = `kube_pod_status_ready{namespace="gitops"}`
	delete(ch.Facts, "metric") // will guess
	ch.Facts["metric"] = "kube_pod_status_ready"
	rep, err := analyze.Run(base, ch)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range rep.Findings {
		if f.Scenario == "prom-zero-match" {
			t.Fatalf("should not fire: %+v", f)
		}
	}
}

func TestDuplicateNodeRejected(t *testing.T) {
	b, _ := golden(t, "rwo-node-loss")
	base, _ := loader.LoadSnapshot(b)
	base.Nodes = append(base.Nodes, base.Nodes[0])
	tmp := filepath.Join(t.TempDir(), "bad.json")
	raw, _ := json.Marshal(base)
	_ = os.WriteFile(tmp, raw, 0o644)
	_, err := loader.LoadSnapshot(tmp)
	if err == nil {
		t.Fatal("expected validation error")
	}
}

func TestDanglingEdgeRejected(t *testing.T) {
	b, _ := golden(t, "rwo-node-loss")
	base, _ := loader.LoadSnapshot(b)
	base.Edges[len(base.Edges)-1].To = "does-not-exist"
	tmp := filepath.Join(t.TempDir(), "bad.json")
	raw, _ := json.Marshal(base)
	_ = os.WriteFile(tmp, raw, 0o644)
	_, err := loader.LoadSnapshot(tmp)
	if err == nil {
		t.Fatal("expected dangling edge error")
	}
}

func TestRollbackUnknownWithoutFindings(t *testing.T) {
	b, c := golden(t, "cni-ip-capacity")
	base, _ := loader.LoadSnapshot(b)
	ch, _ := loader.LoadChange(c)
	base.Meta["pod_ip_capacity_available"] = 10000
	ch.PatchNodes[0].Attributes["replicas"] = 6
	ch.PatchNodes[1].Attributes["replicas"] = 4
	rep, err := analyze.Run(base, ch)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Findings) != 0 {
		// may still have other findings; only check rollback if none
		return
	}
	if rep.Rollback != analyze.RollbackUnknown {
		t.Fatalf("rollback=%s want unknown", rep.Rollback)
	}
}

func TestSemanticDigestStable(t *testing.T) {
	b, c := golden(t, "prom-zero-match")
	base, _ := loader.LoadSnapshot(b)
	ch, _ := loader.LoadChange(c)
	r1, _ := analyze.Run(base, ch)
	r2, _ := analyze.Run(base, ch)
	if r1.SemanticDigest == "" || r1.SemanticDigest != r2.SemanticDigest {
		t.Fatalf("digest unstable: %s vs %s", r1.SemanticDigest, r2.SemanticDigest)
	}
}

// silence unused import if any
var _ = scenario.Finding{}
