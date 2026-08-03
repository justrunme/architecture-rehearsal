package policy_test

import (
	"testing"

	"github.com/justrunme/architecture-rehearsal/internal/policy"
)

func TestBlockHigh(t *testing.T) {
	r := policy.Evaluate(policy.DefaultDocument(), policy.Input{Risk: "critical", Decision: "block"})
	if r.Decision != "block" {
		t.Fatalf("%+v", r)
	}
	r = policy.Evaluate(policy.DefaultDocument(), policy.Input{Risk: "none", Decision: "approve"})
	if r.Decision != "approve" {
		t.Fatalf("%+v", r)
	}
	r = policy.Evaluate(policy.DefaultDocument(), policy.Input{Risk: "medium", Decision: "warn"})
	if r.Decision != "warn" {
		t.Fatalf("%+v", r)
	}
}
