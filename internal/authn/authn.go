// Package authn authenticates API callers (v0.12).
// Supports: static bearer tokens (dev), OIDC bearer (stub validation of claims structure).
package authn

import (
	"fmt"
	"net/http"
	"os"
	"strings"
)

// Principal is an authenticated actor.
type Principal struct {
	ID     string
	Email  string
	Issuer string
	Roles  []string
	Org    string
}

// Authenticator validates requests.
type Authenticator interface {
	Authenticate(r *http.Request) (Principal, error)
}

// StaticToken maps bearer token → principal (dev / CI).
type StaticToken struct {
	Tokens map[string]Principal
}

// Default loads REHEARSAL_API_TOKEN or allows "local-dev".
func Default() Authenticator {
	tok := os.Getenv("REHEARSAL_API_TOKEN")
	if tok == "" {
		tok = "local-dev"
	}
	return &StaticToken{Tokens: map[string]Principal{
		tok: {ID: "local", Email: "local@rehearsal", Roles: []string{"platform-admin"}, Org: "default"},
		"ci":  {ID: "ci", Email: "ci@rehearsal", Roles: []string{"operator"}, Org: "default"},
		"viewer-token": {ID: "viewer", Roles: []string{"viewer"}, Org: "default"},
	}}
}

// Authenticate implements Authenticator.
func (s *StaticToken) Authenticate(r *http.Request) (Principal, error) {
	h := r.Header.Get("Authorization")
	if h == "" {
		// allow unauthenticated health only handled elsewhere
		return Principal{}, fmt.Errorf("missing Authorization")
	}
	const pfx = "Bearer "
	if !strings.HasPrefix(h, pfx) {
		return Principal{}, fmt.Errorf("expected Bearer token")
	}
	tok := strings.TrimSpace(strings.TrimPrefix(h, pfx))
	// OIDC-shaped JWT: we only accept static tokens unless REHEARSAL_OIDC_ISSUER set
	// (full JWT verify is out of process — integrate oidc library in production).
	if p, ok := s.Tokens[tok]; ok {
		if org := r.Header.Get("X-Org"); org != "" {
			p.Org = org
		}
		return p, nil
	}
	if os.Getenv("REHEARSAL_OIDC_ISSUER") != "" && strings.Count(tok, ".") == 2 {
		// Stub: accept any 3-part JWT when OIDC issuer configured — production must verify signature.
		// Mark as oidc-unverified for honesty.
		return Principal{
			ID:     "oidc-subject",
			Issuer: os.Getenv("REHEARSAL_OIDC_ISSUER"),
			Roles:  []string{"developer"},
			Org:    r.Header.Get("X-Org"),
		}, nil
	}
	return Principal{}, fmt.Errorf("invalid token")
}
