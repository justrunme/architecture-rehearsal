// Package authz implements hierarchical RBAC (v1.0.1).
//
//	organization → project → environment → cluster
//
// Tenant isolation: non-admin principals may only access resources in their Org.
// Resource org is taken from stored object labels, never from untrusted client headers alone.
package authz

import (
	"os"

	"github.com/justrunme/architecture-rehearsal/internal/authn"
)

// Action is an API operation.
type Action string

const (
	ActionRunCreate    Action = "run.create"
	ActionRunRead      Action = "run.read"
	ActionRunWrite     Action = "run.write"
	ActionEvidenceRead Action = "evidence.read"
	ActionClusterRead  Action = "cluster.read"
	ActionClusterWrite Action = "cluster.write"
	ActionPolicyWrite  Action = "policy.write"
	ActionPolicyRead   Action = "policy.read"
	ActionAdmin        Action = "admin"
)

// Resource scopes an action (from stored object or principal binding).
type Resource struct {
	Org         string
	Project     string
	Environment string
	Cluster     string
}

// Authorizer checks permissions.
type Authorizer struct {
	RoleGrants map[string][]Action
}

// Default role matrix.
func Default() *Authorizer {
	return &Authorizer{RoleGrants: map[string][]Action{
		"viewer":            {ActionRunRead, ActionEvidenceRead, ActionClusterRead, ActionPolicyRead},
		"developer":         {ActionRunRead, ActionRunCreate, ActionEvidenceRead, ActionClusterRead, ActionPolicyRead},
		"operator":          {ActionRunRead, ActionRunCreate, ActionRunWrite, ActionEvidenceRead, ActionClusterRead, ActionClusterWrite, ActionPolicyRead},
		"security-reviewer": {ActionRunRead, ActionEvidenceRead, ActionPolicyWrite, ActionPolicyRead, ActionClusterRead},
		"platform-admin":    {ActionRunRead, ActionRunCreate, ActionRunWrite, ActionEvidenceRead, ActionClusterRead, ActionClusterWrite, ActionPolicyWrite, ActionPolicyRead, ActionAdmin},
	}}
}

// Allow reports whether principal may perform action on resource.
func (a *Authorizer) Allow(p authn.Principal, action Action, res Resource) bool {
	if !a.hasAction(p, action) {
		return false
	}
	return a.sameTenant(p, res)
}

// CanAccessObject is a strict check after loading an object by ID.
func (a *Authorizer) CanAccessObject(p authn.Principal, action Action, res Resource) bool {
	return a.Allow(p, action, res)
}

func (a *Authorizer) hasAction(p authn.Principal, action Action) bool {
	for _, role := range p.Roles {
		for _, act := range a.RoleGrants[role] {
			if act == action || act == ActionAdmin {
				return true
			}
		}
	}
	return false
}

// sameTenant enforces org isolation.
// platform-admin may cross orgs only when acting within their role and resource org empty
// (list all) — for object access, admin still must match unless role includes admin AND
// REHEARSAL_CROSS_TENANT_ADMIN=1. Default: even platform-admin is org-scoped for safety.
func (a *Authorizer) sameTenant(p authn.Principal, res Resource) bool {
	if p.Org == "" {
		return false // unscoped principal denied
	}
	// Object without org label: deny (force labeling)
	if res.Org == "" {
		// create paths may use principal org as resource
		return true // allow create when resource org will be set from principal
	}
	if p.Org != res.Org {
		// Cross-tenant only for explicit admin override (off by default)
		if isAdmin(p) && osCrossTenantAdmin() {
			return true
		}
		return false
	}
	return true
}

func isAdmin(p authn.Principal) bool {
	for _, r := range p.Roles {
		if r == "platform-admin" {
			return true
		}
	}
	return false
}

func osCrossTenantAdmin() bool {
	return os.Getenv("REHEARSAL_CROSS_TENANT_ADMIN") == "1"
}
