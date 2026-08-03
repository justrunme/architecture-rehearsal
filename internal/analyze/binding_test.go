package analyze_test

import (
	"testing"

	"github.com/justrunme/architecture-rehearsal/internal/analyze"
	"github.com/justrunme/architecture-rehearsal/internal/loader"
	"github.com/justrunme/architecture-rehearsal/internal/verify"
)

func TestReportEmbedsDigests(t *testing.T) {
	base, err := loader.LoadSnapshot("../../examples/golden/cni-ip-capacity/baseline.json")
	if err != nil {
		t.Fatal(err)
	}
	ch, err := loader.LoadChange("../../examples/golden/cni-ip-capacity/change.json")
	if err != nil {
		t.Fatal(err)
	}
	rep, err := analyze.Run(base, ch)
	if err != nil {
		t.Fatal(err)
	}
	if rep.BaselineDigest == "" || rep.ChangeDigest == "" || rep.ProposedDigest == "" {
		t.Fatalf("missing digests: base=%q change=%q proposed=%q", rep.BaselineDigest, rep.ChangeDigest, rep.ProposedDigest)
	}
	if err := analyze.AssertBindings(rep, rep.BaselineDigest, rep.ChangeDigest); err != nil {
		t.Fatal(err)
	}
	// Tamper change digest on report
	bad := *rep
	bad.ChangeDigest = "deadbeef"
	if err := analyze.AssertBindings(&bad, rep.BaselineDigest, rep.ChangeDigest); err == nil {
		t.Fatal("expected binding failure")
	}
}

func TestVerifyRefusesMismatchedReport(t *testing.T) {
	base, _ := loader.LoadSnapshot("../../examples/golden/cni-ip-capacity/baseline.json")
	ch, _ := loader.LoadChange("../../examples/golden/cni-ip-capacity/change.json")
	rep, err := analyze.Run(base, ch)
	if err != nil {
		t.Fatal(err)
	}
	// Load a different change and claim report is for it
	ch2 := *ch
	ch2.ID = "tampered"
	ch2.Title = "tampered title"
	obs := base // dummy
	res := verify.RunWithOptions(rep, obs, verify.Options{Baseline: base, Change: &ch2})
	if res.Outcome != verify.OutcomeDiverged {
		t.Fatalf("want diverged on binding break, got %s checks=%+v", res.Outcome, res.Checks)
	}
	found := false
	for _, c := range res.Checks {
		if c.Name == "report_binding" && !c.Passed {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected report_binding fail: %+v", res.Checks)
	}
}
