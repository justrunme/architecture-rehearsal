// Package policy evaluates organization gate rules over deterministic findings (v0.10).
// Scenarios produce facts; policy decides approve|warn|block|unknown.
package policy

import (
	"fmt"
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
	UnknownAsBlock bool `yaml:"unknownAsBlock" json:"unknownAsBlock"`
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
		UnknownAsBlock: true,
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
	if err := yaml.Unmarshal(raw, &d); err != nil {
		return nil, err
	}
	if len(d.Block) == 0 && len(d.Warn) == 0 {
		return DefaultDocument(), nil
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
		if match(expr, in) {
			res.Decision = "block"
			res.Matched = append(res.Matched, expr)
			return res
		}
	}
	if doc.UnknownAsBlock && (in.Decision == "unknown" || in.Risk == "unknown") {
		res.Decision = "block"
		res.Matched = append(res.Matched, "unknownAsBlock")
		return res
	}
	for _, expr := range doc.Warn {
		if match(expr, in) {
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

func match(expr string, in Input) bool {
	e := strings.TrimSpace(expr)
	// risk in ["critical","high"]
	if strings.HasPrefix(e, "risk in ") {
		return listContains(e, in.Risk)
	}
	if strings.HasPrefix(e, "decision in ") {
		return listContains(e, in.Decision)
	}
	if strings.Contains(e, "==") {
		parts := strings.SplitN(e, "==", 2)
		if len(parts) != 2 {
			return false
		}
		left := strings.TrimSpace(parts[0])
		right := strings.Trim(strings.TrimSpace(parts[1]), `"'`)
		switch left {
		case "risk":
			return in.Risk == right
		case "decision":
			return in.Decision == right
		case "rollback":
			return in.Rollback == right
		}
	}
	if strings.Contains(e, ">") {
		parts := strings.SplitN(e, ">", 2)
		if len(parts) != 2 {
			return false
		}
		left := strings.TrimSpace(parts[0])
		var n int
		_, _ = fmt.Sscanf(strings.TrimSpace(parts[1]), "%d", &n)
		switch left {
		case "requiredMissing":
			return in.RequiredMissing > n
		case "findings":
			return in.Findings > n
		case "criticalPaths":
			return in.CriticalPaths > n
		}
	}
	return false
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
