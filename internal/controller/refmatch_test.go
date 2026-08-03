package controller

import (
	"testing"

	"github.com/justrunme/architecture-rehearsal/api/v1beta1"
	"github.com/justrunme/architecture-rehearsal/internal/run"
)

func TestRefMatchesSandbox(t *testing.T) {
	cases := []struct {
		cr, stored string
		want       bool
	}{
		{"baseline.json", "baseline.json", true},
		{"baseline.json", "/data/baseline.json", true},
		{"fixtures/base.json", "/data/fixtures/base.json", true},
		{"baseline.json", "/data/evil-baseline.json", false},
		{"a.json", "/data/b.json", false},
		{"", "", true},
		{"x", "", false},
		{"", "/data/x", false},
	}
	for _, tc := range cases {
		if got := refMatches(tc.cr, tc.stored); got != tc.want {
			t.Errorf("refMatches(%q,%q)=%v want %v", tc.cr, tc.stored, got, tc.want)
		}
	}
}

func TestSpecMatchesRunSandboxedPaths(t *testing.T) {
	spec := v1beta1.RehearsalRunSpec{
		BaselineRef: "baseline.json",
		ChangeRef:   "change.json",
	}
	rr := run.NewRun("id", "id", run.Spec{
		BaselineRef: "/data/baseline.json",
		ChangeRef:   "/data/change.json",
	})
	if !specMatchesRun(spec, rr) {
		t.Fatal("sandboxed absolute refs must match CR relative refs")
	}
	rr.Spec.ChangeRef = "/data/other.json"
	if specMatchesRun(spec, rr) {
		t.Fatal("different change ref must not match")
	}
}
