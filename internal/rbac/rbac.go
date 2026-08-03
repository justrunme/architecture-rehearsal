// Package rbac provides a minimal policy model for platform ops (v0.7).
// This is configuration-level RBAC, not a network service authenticator.
package rbac

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// Action enumerates gated operations.
type Action string

const (
	ActionAnalyze   Action = "analyze"
	ActionVerify    Action = "verify"
	ActionSnapshot  Action = "snapshot"
	ActionChange    Action = "change"
	ActionStore     Action = "store"
	ActionMerge     Action = "merge"
	ActionAuditRead Action = "audit.read"
	ActionSign      Action = "sign"
)

// Policy maps roles → allowed actions.
type Policy struct {
	Version string              `yaml:"version" json:"version"`
	Roles   map[string][]string `yaml:"roles" json:"roles"`
	// Bindings map actor → role
	Bindings map[string]string `yaml:"bindings" json:"bindings"`
}

// DefaultPolicy is a safe local-dev policy (actor "ci" and "local" get full access).
func DefaultPolicy() *Policy {
	return &Policy{
		Version: "v0.7",
		Roles: map[string][]string{
			"viewer":  {string(ActionAuditRead), string(ActionVerify)},
			"analyst": {string(ActionAnalyze), string(ActionVerify), string(ActionSnapshot), string(ActionChange), string(ActionAuditRead)},
			"admin": {
				string(ActionAnalyze), string(ActionVerify), string(ActionSnapshot),
				string(ActionChange), string(ActionStore), string(ActionMerge),
				string(ActionAuditRead), string(ActionSign),
			},
		},
		Bindings: map[string]string{
			"local": "admin",
			"ci":    "admin",
			"*":     "viewer",
		},
	}
}

// LoadPolicy reads YAML policy or returns default.
func LoadPolicy(path string) (*Policy, error) {
	if path == "" {
		return DefaultPolicy(), nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var p Policy
	if err := yaml.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	if p.Roles == nil {
		return nil, fmt.Errorf("policy has no roles")
	}
	return &p, nil
}

// Allow checks whether actor may perform action.
func (p *Policy) Allow(actor string, action Action) bool {
	if p == nil {
		return true // no policy file → open local mode
	}
	if actor == "" {
		actor = "local"
	}
	role := p.Bindings[actor]
	if role == "" {
		role = p.Bindings["*"]
	}
	if role == "" {
		return false
	}
	allowed := p.Roles[role]
	for _, a := range allowed {
		if a == string(action) || a == "*" {
			return true
		}
	}
	return false
}

// Require returns error if denied.
func (p *Policy) Require(actor string, action Action) error {
	if p.Allow(actor, action) {
		return nil
	}
	return fmt.Errorf("rbac deny: actor=%q action=%q", actor, action)
}

// ActorFromEnv reads REHEARSAL_ACTOR (default local).
func ActorFromEnv() string {
	if a := strings.TrimSpace(os.Getenv("REHEARSAL_ACTOR")); a != "" {
		return a
	}
	return "local"
}
