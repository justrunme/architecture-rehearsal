// Package authz implements hierarchical RBAC (v0.12).
//
//	organization → project → environment → cluster
package authz

import (
	"github.com/justrunme/architecture-rehearsal/internal/authn"
)

// Action is an API operation.
type Action string

const (
	ActionRunCreate     Action = "run.create"
	ActionRunRead       Action = "run.read"
	ActionRunWrite      Action = "run.write"
	ActionEvidenceRead  Action = "evidence.read"
	ActionClusterRead   Action = "cluster.read"
	ActionClusterWrite  Action = "cluster.write"
	ActionPolicyWrite   Action = "policy.write"
	ActionAdmin         Action = "admin"
)

// Resource scopes an action.
type Resource struct {
	Org         string
	Project     string
	Environment string
	Cluster     string
}

// Authorizer checks permissions.
type Authorizer struct {
	// RoleGrants maps role → actions
	RoleGrants map[string][]Action
}

// Default role matrix.
func Default() *Authorizer {
	return &Authorizer{RoleGrants: map[string][]Action{
		"viewer":            {ActionRunRead, ActionEvidenceRead, ActionClusterRead},
		"developer":         {ActionRunRead, ActionRunCreate, ActionEvidenceRead, ActionClusterRead},
		"operator":          {ActionRunRead, ActionRunCreate, ActionRunWrite, ActionEvidenceRead, ActionClusterRead, ActionClusterWrite},
		"security-reviewer": {ActionRunRead, ActionEvidenceRead, ActionPolicyWrite, ActionClusterRead},
		"platform-admin":    {ActionRunRead, ActionRunCreate, ActionRunWrite, ActionEvidenceRead, ActionClusterRead, ActionClusterWrite, ActionPolicyWrite, ActionAdmin},
	}}
}

// Allow reports whether principal may perform action on resource.
func (a *Authorizer) Allow(p authn.Principal, action Action, res Resource) bool {
	for _, role := range p.Roles {
		for _, act := range a.RoleGrants[role] {
			if act == action || act == ActionAdmin {
				// Tenant boundary: if principal has Org set and resource Org set, must match
				if p.Org != "" && res.Org != "" && p.Org != res.Org && role != "platform-admin" {
					return false
				}
				return true
			}
		}
	}
	return false
}
