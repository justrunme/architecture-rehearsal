package rbac_test

import (
	"testing"

	"github.com/justrunme/architecture-rehearsal/internal/rbac"
)

func TestDefaultPolicy(t *testing.T) {
	p := rbac.DefaultPolicy()
	if err := p.Require("local", rbac.ActionAnalyze); err != nil {
		t.Fatal(err)
	}
	if err := p.Require("unknown-user", rbac.ActionSign); err == nil {
		t.Fatal("viewer must not sign")
	}
	if err := p.Require("unknown-user", rbac.ActionAuditRead); err != nil {
		t.Fatal(err)
	}
}
