package chain_test

import (
	"testing"

	"github.com/justrunme/architecture-rehearsal/internal/analyze"
	"github.com/justrunme/architecture-rehearsal/internal/chain"
	"github.com/justrunme/architecture-rehearsal/internal/contract"
	"github.com/justrunme/architecture-rehearsal/internal/graph"
	"github.com/justrunme/architecture-rehearsal/internal/loader"
)

func baseSnap() *graph.Snapshot {
	return &graph.Snapshot{ID: "b", Phase: graph.PhaseBaseline, Nodes: []graph.Node{{ID: "n1", Kind: graph.KindNode, Name: "n1"}}}
}

func changeEnv() *loader.ChangeEnvelope {
	return &loader.ChangeEnvelope{ID: "c", Kind: "k8s-manifest", Title: "t"}
}

func TestChainBreaksOnBaselineMutation(t *testing.T) {
	base := baseSnap()
	ch := changeEnv()
	bd, _ := contract.ComputeDigest(base)
	cd, _ := contract.ComputeDigest(ch)
	rep := &analyze.Report{
		ChangeID: "c", BaselineID: "b", Decision: "block", Risk: "high",
		BaselineDigest: string(bd), ChangeDigest: string(cd), SemanticDigest: "sem-1",
	}
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
	bd, _ := contract.ComputeDigest(base)
	cd, _ := contract.ComputeDigest(ch)
	rep := &analyze.Report{
		ChangeID: "c", BaselineID: "b", Decision: "block", Risk: "high",
		BaselineDigest: string(bd), ChangeDigest: string(cd), SemanticDigest: "sem-1",
	}
	c, err := chain.Build(base, ch, nil, rep, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	ch.Title = "mutated"
	if err := chain.VerifyChain(c, base, ch, rep, nil); err == nil {
		t.Fatal("expected chain break on change mutation")
	}
}

func TestChainBreaksOnReportDigestMutation(t *testing.T) {
	base := baseSnap()
	ch := changeEnv()
	bd, _ := contract.ComputeDigest(base)
	cd, _ := contract.ComputeDigest(ch)
	rep := &analyze.Report{
		ChangeID: "c", BaselineID: "b", Decision: "block", Risk: "high",
		BaselineDigest: string(bd), ChangeDigest: string(cd), SemanticDigest: "sem-1",
	}
	c, err := chain.Build(base, ch, nil, rep, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Tamper chain report digest only
	c.Digests.ReportDigest = "deadbeef"
	if err := chain.VerifyChain(c, base, ch, rep, nil); err == nil {
		t.Fatal("expected chain break on report digest tamper")
	}
}

func TestChainBreaksWhenReportBindingsLie(t *testing.T) {
	// Report claims digests that match chain but live baseline differs —
	// must still fail (no trust of embedded-only binding).
	base := baseSnap()
	ch := changeEnv()
	bd, _ := contract.ComputeDigest(base)
	cd, _ := contract.ComputeDigest(ch)
	rep := &analyze.Report{
		ChangeID: "c", BaselineID: "b", Decision: "block", Risk: "high",
		BaselineDigest: string(bd), ChangeDigest: string(cd), SemanticDigest: "sem-1",
	}
	c, err := chain.Build(base, ch, nil, rep, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Mutate live baseline but leave report.baselineDigest pointing at old
	base.Nodes = append(base.Nodes, graph.Node{ID: "evil", Kind: graph.KindNode, Name: "evil"})
	if err := chain.VerifyChain(c, base, ch, rep, nil); err == nil {
		t.Fatal("must not trust embedded digest when live object mutated")
	}
}

func TestChainOKWhenUnchanged(t *testing.T) {
	base := baseSnap()
	ch := changeEnv()
	bd, _ := contract.ComputeDigest(base)
	cd, _ := contract.ComputeDigest(ch)
	rep := &analyze.Report{
		ChangeID: "c", BaselineID: "b", Decision: "block", Risk: "high",
		BaselineDigest: string(bd), ChangeDigest: string(cd), SemanticDigest: "sem-1",
	}
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
	bd, _ := contract.ComputeDigest(base)
	cd, _ := contract.ComputeDigest(ch)
	rep := &analyze.Report{
		ChangeID: "c", BaselineID: "b", Decision: "approve", Risk: "low",
		BaselineDigest: string(bd), ChangeDigest: string(cd), SemanticDigest: "sem-obs",
	}
	c, err := chain.Build(base, ch, nil, rep, obs, nil)
	if err != nil {
		t.Fatal(err)
	}
	obs.Nodes = append(obs.Nodes, graph.Node{ID: "x", Kind: graph.KindNode, Name: "x"})
	if err := chain.VerifyChain(c, base, ch, rep, obs); err == nil {
		t.Fatal("expected observed break")
	}
}
