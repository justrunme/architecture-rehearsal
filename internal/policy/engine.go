// Package policy evaluates organization gate rules over deterministic findings (v0.10).
// Scenarios produce facts; policy decides approve|warn|block|unknown.
package policy

import (
	"bytes"
	"fmt"
	"log"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// Document is a RehearsalPolicy.
type Document struct {
	APIVersion string   `yaml:"apiVersion" json:"apiVersion"`
	Kind       string   `yaml:"kind" json:"kind"`
	Block      []string `yaml:"block" json:"block"`
	Warn       []string `yaml:"warn" json:"warn"`
	// UnknownAsBlock treats unknown decision as block.
	// Nil (omitted in YAML) means true (fail-closed). Explicit false is allowed but logged.
	UnknownAsBlock *bool `yaml:"unknownAsBlock" json:"unknownAsBlock"`
}

// Input is the fact set from analysis.
type Input struct {
	Risk            string
	Decision        string
	Rollback        string
	RequiredMissing int
	Findings        int
	CriticalPaths   int
}

// Result is the policy decision.
type Result struct {
	Decision string   `json:"decision"`
	Matched  []string `json:"matched,omitempty"`
	Source   string   `json:"source"`
}

func boolPtr(v bool) *bool { return &v }

// unknownAsBlockEffective returns the effective unknownAsBlock value.
// Omitted (nil) defaults to true (fail-closed).
func (d *Document) unknownAsBlockEffective() bool {
	if d == nil || d.UnknownAsBlock == nil {
		return true
	}
	return *d.UnknownAsBlock
}

// DefaultDocument blocks high/critical and missing requirements.
func DefaultDocument() *Document {
	return &Document{
		APIVersion: "rehearsal.io/v1beta1",
		Kind:       "RehearsalPolicy",
		Block: []string{
			`risk in ["critical","high"]`,
			`decision == "block"`,
			`rollback == "unavailable"`,
			`requiredMissing > 0`,
		},
		Warn: []string{
			`risk == "medium"`,
			`decision == "warn"`,
		},
		UnknownAsBlock: boolPtr(true),
	}
}

// Load reads YAML policy or returns default.
func Load(path string) (*Document, error) {
	if path == "" {
		return DefaultDocument(), nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var d Document
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&d); err != nil {
		return nil, fmt.Errorf("policy decode: %w", err)
	}
	if len(d.Block) == 0 && len(d.Warn) == 0 {
		return DefaultDocument(), nil
	}
	if d.UnknownAsBlock == nil {
		d.UnknownAsBlock = boolPtr(true)
	} else if !*d.UnknownAsBlock {
		log.Printf("warning: policy %s sets unknownAsBlock: false; UNKNOWN decisions will be approved", path)
	}
	for _, expr := range d.Block {
		if err := validateExpr(expr); err != nil {
			return nil, fmt.Errorf("policy block expression: %w", err)
		}
	}
	for _, expr := range d.Warn {
		if err := validateExpr(expr); err != nil {
			return nil, fmt.Errorf("policy warn expression: %w", err)
		}
	}
	return &d, nil
}

// Evaluate applies simplified expression rules (subset of CEL-like syntax).
func Evaluate(doc *Document, in Input) Result {
	if doc == nil {
		doc = DefaultDocument()
	}
	res := Result{Decision: "approve", Source: "policy"}
	for _, expr := range doc.Block {
		ok, err := match(expr, in)
		if err != nil {
			// Fail closed: invalid expression reached at evaluate time → block.
			res.Decision = "block"
			res.Matched = append(res.Matched, "invalid:"+expr)
			return res
		}
		if ok {
			res.Decision = "block"
			res.Matched = append(res.Matched, expr)
			return res
		}
	}
	if doc.unknownAsBlockEffective() && (in.Decision == "unknown" || in.Risk == "unknown") {
		res.Decision = "block"
		res.Matched = append(res.Matched, "unknownAsBlock")
		return res
	}
	for _, expr := range doc.Warn {
		ok, err := match(expr, in)
		if err != nil {
			res.Decision = "block"
			res.Matched = append(res.Matched, "invalid:"+expr)
			return res
		}
		if ok {
			res.Decision = "warn"
			res.Matched = append(res.Matched, expr)
			return res
		}
	}
	// Preserve analyze unknown if not blocked
	if in.Decision == "unknown" {
		res.Decision = "unknown"
	}
	return res
}

func validateExpr(expr string) error {
	e := strings.TrimSpace(expr)
	if e == "" {
		return fmt.Errorf("empty expression")
	}
	if strings.HasPrefix(e, "risk in ") || strings.HasPrefix(e, "decision in ") {
		i := strings.Index(e, "[")
		j := strings.Index(e, "]")
		if i < 0 || j <= i {
			return fmt.Errorf("invalid list expression %q", expr)
		}
		return nil
	}
	if strings.Contains(e, "==") {
		parts := strings.SplitN(e, "==", 2)
		if len(parts) != 2 {
			return fmt.Errorf("invalid equality expression %q", expr)
		}
		left := strings.TrimSpace(parts[0])
		right := strings.TrimSpace(parts[1])
		if right == "" {
			return fmt.Errorf("missing right-hand side in expression %q", expr)
		}
		switch left {
		case "risk", "decision", "rollback":
			return nil
		default:
			return fmt.Errorf("unknown field %q in expression %q", left, expr)
		}
	}
	if strings.Contains(e, ">") {
		parts := strings.SplitN(e, ">", 2)
		if len(parts) != 2 {
			return fmt.Errorf("invalid comparison expression %q", expr)
		}
		left := strings.TrimSpace(parts[0])
		var n int
		if _, err := fmt.Sscanf(strings.TrimSpace(parts[1]), "%d", &n); err != nil {
			return fmt.Errorf("invalid integer in expression %q", expr)
		}
		switch left {
		case "requiredMissing", "findings", "criticalPaths":
			return nil
		default:
			return fmt.Errorf("unknown field %q in expression %q", left, expr)
		}
	}
	return fmt.Errorf("unknown expression %q", expr)
}

func match(expr string, in Input) (bool, error) {
	if err := validateExpr(expr); err != nil {
		return false, err
	}
	e := strings.TrimSpace(expr)
	// risk in ["critical","high"]
	if strings.HasPrefix(e, "risk in ") {
		return listContains(e, in.Risk), nil
	}
	if strings.HasPrefix(e, "decision in ") {
		return listContains(e, in.Decision), nil
	}
	if strings.Contains(e, "==") {
		parts := strings.SplitN(e, "==", 2)
		left := strings.TrimSpace(parts[0])
		right := strings.Trim(strings.TrimSpace(parts[1]), `"'`)
		switch left {
		case "risk":
			return in.Risk == right, nil
		case "decision":
			return in.Decision == right, nil
		case "rollback":
			return in.Rollback == right, nil
		}
	}
	if strings.Contains(e, ">") {
		parts := strings.SplitN(e, ">", 2)
		left := strings.TrimSpace(parts[0])
		var n int
		_, _ = fmt.Sscanf(strings.TrimSpace(parts[1]), "%d", &n)
		switch left {
		case "requiredMissing":
			return in.RequiredMissing > n, nil
		case "findings":
			return in.Findings > n, nil
		case "criticalPaths":
			return in.CriticalPaths > n, nil
		}
	}
	// Unreachable when validateExpr is kept in sync; fail closed if not.
	return false, fmt.Errorf("unknown expression %q", expr)
}

func listContains(expr, val string) bool {
	// risk in ["critical","high"]
	i := strings.Index(expr, "[")
	j := strings.Index(expr, "]")
	if i < 0 || j < i {
		return false
	}
	inner := expr[i+1 : j]
	for _, p := range strings.Split(inner, ",") {
		p = strings.Trim(strings.TrimSpace(p), `"'`)
		if p == val {
			return true
		}
	}
	return false
}
