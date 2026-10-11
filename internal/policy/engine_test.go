package policy_test

import (
	"os"
	"path/filepath"
	"strings"
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

func writePolicy(t *testing.T, name, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// Negative: custom YAML omitting unknownAsBlock must NOT approve UNKNOWN
// (Go zero-value false would incorrectly approve).
func TestLoad_OmittedUnknownAsBlock_BlocksUnknown(t *testing.T) {
	path := writePolicy(t, "policy.yaml", `
apiVersion: rehearsal.io/v1beta1
kind: RehearsalPolicy
block:
  - risk in ["critical","high"]
warn:
  - risk == "medium"
`)
	doc, err := policy.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if doc.UnknownAsBlock == nil || !*doc.UnknownAsBlock {
		t.Fatalf("omitted unknownAsBlock must default to true, got %#v", doc.UnknownAsBlock)
	}
	r := policy.Evaluate(doc, policy.Input{Risk: "low", Decision: "unknown"})
	if r.Decision != "block" {
		t.Fatalf("omitted unknownAsBlock must block UNKNOWN, got %+v", r)
	}
}

// Positive control: explicit unknownAsBlock:false still approves UNKNOWN.
func TestLoad_ExplicitUnknownAsBlockFalse_ApprovesUnknown(t *testing.T) {
	path := writePolicy(t, "policy.yaml", `
apiVersion: rehearsal.io/v1beta1
kind: RehearsalPolicy
unknownAsBlock: false
block:
  - risk in ["critical","high"]
warn:
  - risk == "medium"
`)
	doc, err := policy.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if doc.UnknownAsBlock == nil || *doc.UnknownAsBlock {
		t.Fatalf("explicit false must be preserved, got %#v", doc.UnknownAsBlock)
	}
	r := policy.Evaluate(doc, policy.Input{Risk: "low", Decision: "unknown"})
	if r.Decision != "unknown" {
		t.Fatalf("explicit unknownAsBlock:false must approve/pass through UNKNOWN, got %+v", r)
	}
}

// Negative: typo'd / unknown expression must fail at Load, not silently match=false.
func TestLoad_UnknownExpression_ReturnsError(t *testing.T) {
	path := writePolicy(t, "policy.yaml", `
apiVersion: rehearsal.io/v1beta1
kind: RehearsalPolicy
unknownAsBlock: true
block:
  - rysk == "critical"
warn:
  - risk == "medium"
`)
	_, err := policy.Load(path)
	if err == nil {
		t.Fatal("expected Load error for typo'd expression, got nil")
	}
	if !strings.Contains(err.Error(), "rysk") && !strings.Contains(err.Error(), "unknown") {
		t.Fatalf("error should mention unknown/typo field, got: %v", err)
	}
}

// Negative: unknown YAML keys must be rejected (KnownFields).
func TestLoad_UnknownYAMLField_ReturnsError(t *testing.T) {
	path := writePolicy(t, "policy.yaml", `
apiVersion: rehearsal.io/v1beta1
kind: RehearsalPolicy
unknownAsBlock: true
blok:
  - risk in ["critical","high"]
`)
	_, err := policy.Load(path)
	if err == nil {
		t.Fatal("expected Load error for unknown YAML field, got nil")
	}
}

// Negative: if an invalid expression is still reached at Evaluate, fail closed (block).
func TestEvaluate_InvalidExpression_FailClosed(t *testing.T) {
	falseVal := false
	doc := &policy.Document{
		UnknownAsBlock: &falseVal,
		Block:          []string{`notARealField == "x"`},
	}
	r := policy.Evaluate(doc, policy.Input{Risk: "low", Decision: "approve"})
	if r.Decision != "block" {
		t.Fatalf("invalid expression must fail closed to block, got %+v", r)
	}
}
