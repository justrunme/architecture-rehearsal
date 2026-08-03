// Package authn authenticates API callers (v1.0.1 security correction).
//
// Production rules:
//   - Exactly one shared API token from REHEARSAL_API_TOKEN (required to serve).
//   - Optional REHEARSAL_API_TOKEN_FILE for secret mounts.
//   - Optional JSON map REHEARSAL_API_TOKENS for multi-token principals (token→principal).
//   - No hardcoded ci/viewer-token/local-dev in production.
//   - OIDC is NOT stub-accepted; set REHEARSAL_OIDC_ISSUER only after real verifier is wired.
//   - Client headers MUST NOT override Principal.Org.
package authn

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
)

// Principal is an authenticated actor.
// Org is bound at token issue time — never from client X-Org.
type Principal struct {
	ID     string   `json:"id"`
	Email  string   `json:"email,omitempty"`
	Issuer string   `json:"issuer,omitempty"`
	Roles  []string `json:"roles"`
	Org    string   `json:"org"`
}

// Authenticator validates requests.
type Authenticator interface {
	Authenticate(r *http.Request) (Principal, error)
}

// StaticToken maps bearer token → principal.
type StaticToken struct {
	Tokens map[string]Principal
}

// Config from environment.
type Config struct {
	// RequireToken if true, empty token config is an error (serve-time).
	RequireToken bool
	// AllowInsecureDev enables token "local-dev" only when REHEARSAL_ALLOW_INSECURE_DEV=1.
	AllowInsecureDev bool
}

// FromEnv builds authenticator from environment.
// Returns error if production config is missing (no token).
func FromEnv(cfg Config) (*StaticToken, error) {
	tokens := map[string]Principal{}

	// Multi-token JSON: {"tok1":{"id":"a","org":"o","roles":["viewer"]},...}
	if raw := strings.TrimSpace(os.Getenv("REHEARSAL_API_TOKENS")); raw != "" {
		var m map[string]Principal
		if err := json.Unmarshal([]byte(raw), &m); err != nil {
			return nil, fmt.Errorf("REHEARSAL_API_TOKENS: %w", err)
		}
		for k, p := range m {
			if k == "" {
				continue
			}
			if p.ID == "" {
				p.ID = "token-user"
			}
			if p.Org == "" {
				p.Org = "default"
			}
			if len(p.Roles) == 0 {
				p.Roles = []string{"viewer"}
			}
			tokens[k] = p
		}
	}

	// Single primary token
	tok := strings.TrimSpace(os.Getenv("REHEARSAL_API_TOKEN"))
	if tok == "" {
		if p := strings.TrimSpace(os.Getenv("REHEARSAL_API_TOKEN_FILE")); p != "" {
			b, err := os.ReadFile(p)
			if err != nil {
				return nil, fmt.Errorf("REHEARSAL_API_TOKEN_FILE: %w", err)
			}
			tok = strings.TrimSpace(string(b))
		}
	}
	if tok != "" {
		org := strings.TrimSpace(os.Getenv("REHEARSAL_API_ORG"))
		if org == "" {
			org = "default"
		}
		role := strings.TrimSpace(os.Getenv("REHEARSAL_API_ROLE"))
		if role == "" {
			role = "platform-admin"
		}
		tokens[tok] = Principal{
			ID:    "api-token",
			Email: "api@rehearsal.local",
			Roles: []string{role},
			Org:   org,
		}
	}

	// Explicit insecure dev only
	if cfg.AllowInsecureDev || os.Getenv("REHEARSAL_ALLOW_INSECURE_DEV") == "1" {
		if _, ok := tokens["local-dev"]; !ok {
			tokens["local-dev"] = Principal{
				ID: "local-dev", Roles: []string{"platform-admin"}, Org: "default",
			}
		}
	}

	if len(tokens) == 0 {
		if cfg.RequireToken {
			return nil, fmt.Errorf("refusing to start API: set REHEARSAL_API_TOKEN (or REHEARSAL_API_TOKENS / REHEARSAL_ALLOW_INSECURE_DEV=1 for local only)")
		}
		return nil, fmt.Errorf("no API tokens configured")
	}

	// Never ship hardcoded shared tokens in the map.
	delete(tokens, "ci")
	delete(tokens, "viewer-token")

	return &StaticToken{Tokens: tokens}, nil
}

// Default is for tests only — enables insecure local-dev when env empty.
// Production serve path must use FromEnv(Config{RequireToken: true}).
func Default() Authenticator {
	a, err := FromEnv(Config{RequireToken: false, AllowInsecureDev: true})
	if err != nil {
		// last resort test-only
		return &StaticToken{Tokens: map[string]Principal{
			"local-dev": {ID: "local-dev", Roles: []string{"platform-admin"}, Org: "default"},
		}}
	}
	return a
}

// Authenticate implements Authenticator.
// Client X-Org / X-Project headers do NOT mutate the principal.
func (s *StaticToken) Authenticate(r *http.Request) (Principal, error) {
	if s == nil || len(s.Tokens) == 0 {
		return Principal{}, fmt.Errorf("authenticator not configured")
	}
	h := r.Header.Get("Authorization")
	if h == "" {
		return Principal{}, fmt.Errorf("missing Authorization")
	}
	const pfx = "Bearer "
	if !strings.HasPrefix(h, pfx) {
		return Principal{}, fmt.Errorf("expected Bearer token")
	}
	tok := strings.TrimSpace(strings.TrimPrefix(h, pfx))
	if tok == "" {
		return Principal{}, fmt.Errorf("empty bearer token")
	}

	// Constant-time compare against known tokens (best-effort over map).
	for known, p := range s.Tokens {
		if subtle.ConstantTimeCompare([]byte(known), []byte(tok)) == 1 {
			// Return a copy — never allow header override of Org.
			out := p
			if out.Org == "" {
				out.Org = "default"
			}
			return out, nil
		}
	}

	return Principal{}, fmt.Errorf("invalid token")
}

// FromEnvAuthenticator returns StaticToken and optional OIDC chain for serve.
// When REHEARSAL_OIDC_ISSUER is set, JWTs are verified against JWKS (RS256).
// Static tokens still work for self-hosted. Unsigned/malformed JWT fails verification.
func FromEnvAuthenticator(cfg Config) (Authenticator, error) {
	static, err := FromEnv(cfg)
	if err != nil {
		// OIDC-only mode: allow empty static if issuer set
		if os.Getenv("REHEARSAL_OIDC_ISSUER") == "" {
			return nil, err
		}
		static = nil
	}
	issuer := strings.TrimSpace(os.Getenv("REHEARSAL_OIDC_ISSUER"))
	if issuer == "" {
		if static == nil {
			return nil, fmt.Errorf("no authenticator")
		}
		return static, nil
	}
	jwks := strings.TrimSpace(os.Getenv("REHEARSAL_OIDC_JWKS_URL"))
	aud := strings.TrimSpace(os.Getenv("REHEARSAL_OIDC_AUDIENCE"))
	oidc, err := NewOIDCVerifier(OIDCConfig{Issuer: issuer, JWKSURL: jwks, Audience: aud})
	if err != nil {
		return nil, err
	}
	var auths []Authenticator
	if static != nil {
		auths = append(auths, static)
	}
	auths = append(auths, oidc)
	return &ChainAuthenticator{Auths: auths}, nil
}
