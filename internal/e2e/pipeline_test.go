// Package e2e exercises the v0.4 iron path against fixture YAML (no live cluster).
package e2e_test

import (
	"path/filepath"
	"runtime"
	"testing"

	"github.com/justrunme/architecture-rehearsal/internal/analyze"
	"github.com/justrunme/architecture-rehearsal/internal/change"
	"github.com/justrunme/architecture-rehearsal/internal/collect"
	"github.com/justrunme/architecture-rehearsal/internal/graph"
	"github.com/justrunme/architecture-rehearsal/internal/verify"
)

func fixtureRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	// internal/e2e → repo root
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", "examples", "e2e-pipeline"))
}

func TestE2EPipeline_DumpToVerify(t *testing.T) {
	root := fixtureRoot(t)

	// 1. Baseline from kubectl List dump
	base, err := collect.K8sFromManifests(nil, filepath.Join(root, "cluster-dump"), collect.K8sOptions{
		ClusterName: "acme-prod",
		Phase:       graph.PhaseBaseline,
	})
	if err != nil {
		t.Fatalf("collect baseline: %v", err)
	}
	if base.Meta["pod_ip_capacity_available"] == nil {
		t.Fatal("expected pod_ip_capacity_available derived from node allocatablePods")
	}
	kinds := map[graph.Kind]int{}
	for _, n := range base.Nodes {
		kinds[n.Kind]++
	}
	if kinds[graph.KindWorkload] < 3 || kinds[graph.KindNode] < 2 || kinds[graph.KindPVC] < 1 {
		t.Fatalf("incomplete graph kinds=%v nodes=%d edges=%d", kinds, len(base.Nodes), len(base.Edges))
	}
	// Edges: service→workload, pdb→workload, workload→pvc
	hasRoutes, hasPVC, hasPDB := false, false, false
	for _, e := range base.Edges {
		switch e.Rel {
		case graph.RelRoutesTo:
			hasRoutes = true
		case graph.RelBindsVolume:
			hasPVC = true
		case graph.RelProtectedBy:
			hasPDB = true
		}
	}
	if !hasRoutes || !hasPVC || !hasPDB {
		t.Fatalf("missing expected edges routes=%v pvc=%v pdb=%v edges=%d", hasRoutes, hasPVC, hasPDB, len(base.Edges))
	}

	// 2. Scoped change from rendered chart
	ch, err := change.FromManifestsDiff(base, filepath.Join(root, "rendered-chart"),
		"change-helm-scale-payments-e2e", "Helm upgrade: scale payments",
		change.ManifestScope{Namespaces: []string{"payments"}})
	if err != nil {
		t.Fatalf("compile change: %v", err)
	}
	if len(ch.RemovedNodes) != 0 {
		t.Fatalf("scoped change must not remove nodes without allowRemove: %v", ch.RemovedNodes)
	}
	if len(ch.PatchNodes) < 2 {
		t.Fatalf("expected payments scale patches, got %v", ch.PatchNodes)
	}
	for _, p := range ch.PatchNodes {
		if len(p.ID) < len("workload/payments/") || p.ID[:len("workload/payments/")] != "workload/payments/" {
			t.Fatalf("patch outside payments scope: %s", p.ID)
		}
	}

	// 3. Analyze → block on CNI
	rep, err := analyze.Run(base, ch)
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	if rep.Decision != analyze.DecisionBlock {
		t.Fatalf("decision=%s risk=%s findings=%+v missing=%v",
			rep.Decision, rep.Risk, rep.Findings, rep.Coverage.RequiredMissing)
	}
	foundCNI := false
	for _, f := range rep.PredictedFailures {
		if f == "cni-ip-capacity" {
			foundCNI = true
		}
	}
	if !foundCNI {
		t.Fatalf("expected cni-ip-capacity in predicted_failures, got %v", rep.PredictedFailures)
	}
	if rep.Version != analyze.Version {
		t.Fatalf("version=%s want %s", rep.Version, analyze.Version)
	}

	// 4. Observed snapshot — independent evidence from Pending pods (annotation optional)
	meta, err := collect.LoadMetaFile(filepath.Join(root, "observed-meta.json"))
	if err != nil {
		t.Fatal(err)
	}
	obs, err := collect.K8sFromManifests(nil, filepath.Join(root, "observed-dump"), collect.K8sOptions{
		ClusterName: "acme-prod",
		Phase:       graph.PhaseObserved,
		ExtraMeta:   meta,
	})
	if err != nil {
		t.Fatalf("collect observed: %v", err)
	}
	if obs.Phase != graph.PhaseObserved {
		t.Fatalf("phase=%s", obs.Phase)
	}

	// 5. Verify with baseline+change for identity/delta (independent of annotation)
	vres := verify.RunWithOptions(rep, obs, verify.Options{Baseline: base, Change: ch})
	if vres.Outcome != verify.OutcomeVerified {
		t.Fatalf("verify outcome=%s summary=%s checks=%+v", vres.Outcome, vres.Summary, vres.Checks)
	}
	if vres.DeployedChangeDigest == "" {
		t.Fatal("expected deployed change digest")
	}
}
