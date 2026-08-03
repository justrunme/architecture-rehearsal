package chain_test

import (
	"testing"

	"github.com/justrunme/architecture-rehearsal/internal/analyze"
	"github.com/justrunme/architecture-rehearsal/internal/chain"
	"github.com/justrunme/architecture-rehearsal/internal/graph"
	"github.com/justrunme/architecture-rehearsal/internal/loader"
)

func TestChainBreaksOnMutation(t *testing.T) {
	base := &graph.Snapshot{ID: "b", Phase: graph.PhaseBaseline, Nodes: []graph.Node{{ID: "n1", Kind: graph.KindNode, Name: "n1"}}}
	ch := &loader.ChangeEnvelope{ID: "c", Kind: "k8s-manifest", Title: "t"}
	rep := &analyze.Report{ChangeID: "c", BaselineID: "b", Decision: "block", Risk: "high"}
	c, err := chain.Build(base, ch, nil, rep, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	// mutate baseline
	base.Nodes = append(base.Nodes, graph.Node{ID: "n2", Kind: graph.KindNode, Name: "n2"})
	if err := chain.VerifyChain(c, base, ch, rep, nil); err == nil {
		t.Fatal("expected chain break")
	}
}
