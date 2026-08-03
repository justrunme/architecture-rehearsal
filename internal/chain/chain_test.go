package chain_test

import (
	"testing"

	"github.com/justrunme/architecture-rehearsal/internal/analyze"
	"github.com/justrunme/architecture-rehearsal/internal/chain"
	"github.com/justrunme/architecture-rehearsal/internal/contract"
	"github.com/justrunme/architecture-rehearsal/internal/graph"
	"github.com/justrunme/architecture-rehearsal/internal/loader"
	"github.com/justrunme/architecture-rehearsal/internal/scenario"
)

func baseSnap() *graph.Snapshot {
	return &graph.Snapshot{ID: "b", Phase: graph.PhaseBaseline, Nodes: []graph.Node{{ID: "n1", Kind: graph.KindNode, Name: "n1"}}}
}

func changeEnv() *loader.ChangeEnvelope {
	return &loader.ChangeEnvelope{ID: "c", Kind: "k8s-manifest", Title: "t"}
}

func boundReport(base *graph.Snapshot, ch *loader.ChangeEnvelope) *analyze.Report {
	bd, _ := contract.ComputeDigest(base)
	cd, _ := contract.ComputeDigest(ch)
	rep := &analyze.Report{
		ChangeID: "c", BaselineID: "b", Decision: "block", Risk: "high",
		BaselineDigest: string(bd), ChangeDigest: string(cd),
		Rollback: "unknown", PredictedFailures: []string{"rwo-node-loss"},
		Findings: []scenario.Finding{{ID: "f1", Scenario: "rwo", Risk: "high", Title: "t"}},
	}
	rep.SemanticDigest = analyze.ComputeSemanticDigest(rep)
	return rep
}

func TestChainBreaksOnBaselineMutation(t *testing.T) {
	base := baseSnap()
	ch := changeEnv()
	rep := boundReport(base, ch)
	c, err := chain.Build(base, ch, nil, rep, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	base.Nodes = append(base.Nodes, graph.Node{ID: "n2", Kind: graph.KindNode, Name: "n2"})
	if err := chain.VerifyChain(c, base, ch, rep, nil); err == nil {
		t.Fatal("expected chain break on baseline mutation")
	}
}

func TestChainBreaksOnChangeMutation(t *testing.T) {
	base := baseSnap()
	ch := changeEnv()
	rep := boundReport(base, ch)
	c, err := chain.Build(base, ch, nil, rep, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	ch.Title = "mutated"
	if err := chain.VerifyChain(c, base, ch, rep, nil); err == nil {
		t.Fatal("expected chain break on change mutation")
	}
}

func TestReportTamperKeepsStaleSemanticDigest(t *testing.T) {
	// Core product promise: mutate decision/risk/findings while leaving SemanticDigest
	// field unchanged must fail verification.
	base := baseSnap()
	ch := changeEnv()
	rep := boundReport(base, ch)
	c, err := chain.Build(base, ch, nil, rep, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := chain.VerifyChain(c, base, ch, rep, nil); err != nil {
		t.Fatalf("clean should pass: %v", err)
	}

	cases := []struct {
		name string
		mut  func(*analyze.Report)
	}{
		{"decision", func(r *analyze.Report) { r.Decision = "approve" }},
		{"risk", func(r *analyze.Report) { r.Risk = "low" }},
		{"findings", func(r *analyze.Report) {
			r.Findings = append(r.Findings, scenario.Finding{ID: "evil", Scenario: "x", Risk: "critical", Title: "evil"})
		}},
		{"rollback", func(r *analyze.Report) { r.Rollback = "unavailable" }},
		{"predictedFailures", func(r *analyze.Report) {
			r.PredictedFailures = append(r.PredictedFailures, "injected")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// fresh copy of report with original semantic digest field
			rep2 := *rep
			rep2.Findings = append([]scenario.Finding{}, rep.Findings...)
			rep2.PredictedFailures = append([]string{}, rep.PredictedFailures...)
			oldSem := rep2.SemanticDigest
			tc.mut(&rep2)
			// attacker keeps old semanticDigest
			rep2.SemanticDigest = oldSem
			if err := chain.VerifyChain(c, base, ch, &rep2, nil); err == nil {
				t.Fatalf("tamper %s with stale semanticDigest must fail", tc.name)
			}
		})
	}
}

func TestChainOKWhenUnchanged(t *testing.T) {
	base := baseSnap()
	ch := changeEnv()
	rep := boundReport(base, ch)
	c, err := chain.Build(base, ch, nil, rep, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := chain.VerifyChain(c, base, ch, rep, nil); err != nil {
		t.Fatal(err)
	}
}

func TestObservedMutationBreaks(t *testing.T) {
	base := baseSnap()
	ch := changeEnv()
	obs := &graph.Snapshot{ID: "o", Phase: graph.PhaseObserved, Nodes: []graph.Node{{ID: "n1", Kind: graph.KindNode, Name: "n1"}}}
	rep := boundReport(base, ch)
	c, err := chain.Build(base, ch, nil, rep, obs, nil)
	if err != nil {
		t.Fatal(err)
	}
	obs.Nodes = append(obs.Nodes, graph.Node{ID: "x", Kind: graph.KindNode, Name: "x"})
	if err := chain.VerifyChain(c, base, ch, rep, obs); err == nil {
		t.Fatal("expected observed break")
	}
}
