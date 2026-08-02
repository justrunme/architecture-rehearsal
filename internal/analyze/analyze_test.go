package analyze_test

import (
	"path/filepath"
	"runtime"
	"testing"

	"github.com/justrunme/architecture-rehearsal/internal/analyze"
	"github.com/justrunme/architecture-rehearsal/internal/loader"
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
	rep := analyze.Run(base, ch)
	if rep.Risk != analyze.RiskCritical && rep.Risk != analyze.RiskHigh {
		t.Fatalf("risk=%s findings=%+v", rep.Risk, rep.Findings)
	}
	if rep.Decision != analyze.DecisionBlock {
		t.Fatalf("decision=%s", rep.Decision)
	}
	if len(rep.Findings) == 0 {
		t.Fatal("expected findings")
	}
}

func TestGoldenCNI(t *testing.T) {
	b, c := golden(t, "cni-ip-capacity")
	base, _ := loader.LoadSnapshot(b)
	ch, _ := loader.LoadChange(c)
	rep := analyze.Run(base, ch)
	if rep.Risk != analyze.RiskCritical && rep.Risk != analyze.RiskHigh {
		t.Fatalf("risk=%s", rep.Risk)
	}
	found := false
	for _, f := range rep.Findings {
		if f.Scenario == "cni-ip-capacity" {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing cni finding: %+v", rep.Findings)
	}
}

func TestGoldenProm(t *testing.T) {
	b, c := golden(t, "prom-zero-match")
	base, _ := loader.LoadSnapshot(b)
	ch, _ := loader.LoadChange(c)
	rep := analyze.Run(base, ch)
	if len(rep.Findings) == 0 {
		t.Fatal("expected prom-zero-match finding")
	}
	if rep.Findings[0].Scenario != "prom-zero-match" {
		t.Fatalf("%+v", rep.Findings)
	}
}
