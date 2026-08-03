package store_test

import (
	"path/filepath"
	"testing"

	"github.com/justrunme/architecture-rehearsal/internal/analyze"
	"github.com/justrunme/architecture-rehearsal/internal/store"
)

func TestSaveListAudit(t *testing.T) {
	root := t.TempDir()
	st, err := store.NewFS(root)
	if err != nil {
		t.Fatal(err)
	}
	rep := &analyze.Report{
		Version: "0.7.0", ChangeID: "c1", BaselineID: "b1",
		Decision: analyze.DecisionBlock, Risk: analyze.RiskCritical,
		SemanticDigest: "abc",
	}
	path, err := st.SaveFromReport(rep, filepath.Join(root, "ev"), "ci")
	if err != nil {
		t.Fatal(err)
	}
	if path == "" {
		t.Fatal("empty path")
	}
	runs, err := st.ListRuns()
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs=%v err=%v", runs, err)
	}
	if runs[0].Decision != analyze.DecisionBlock {
		t.Fatalf("%+v", runs[0])
	}
	evs, err := st.ReadAudit(10)
	if err != nil || len(evs) < 1 {
		t.Fatalf("audit=%v err=%v", evs, err)
	}
}
