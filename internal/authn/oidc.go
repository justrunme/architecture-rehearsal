package authn

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// ChainAuthenticator tries authenticators in order.
type ChainAuthenticator struct {
	Auths []Authenticator
}

func (c *ChainAuthenticator) Authenticate(r *http.Request) (Principal, error) {
	var last error
	for _, a := range c.Auths {
		if a == nil {
			continue
		}
		p, err := a.Authenticate(r)
		if err == nil {
			return p, nil
		}
		last = err
	}
	if last != nil {
		return Principal{}, last
	}
	return Principal{}, fmt.Errorf("no authenticator configured")
}

// OIDCConfig for JWKS-based JWT verification (strict).
type OIDCConfig struct {
	Issuer   string // required
	Audience string // required for production
	JWKSURL  string
	ClaimOrg string
	// RequireAudience if true, Audience must be set and match.
	RequireAudience bool
	HTTPClient      *http.Client
}

// OIDCVerifier validates RS256 JWTs against JWKS with strict claim requirements.
type OIDCVerifier struct {
	cfg     OIDCConfig
	mu      sync.RWMutex
	keys    map[string]*rsa.PublicKey
	fetched time.Time
}

// NewOIDCVerifier creates a verifier. Issuer is required; audience required unless REHEARSAL_OIDC_ALLOW_NO_AUD=1.
func NewOIDCVerifier(cfg OIDCConfig) (*OIDCVerifier, error) {
	if cfg.Issuer == "" {
		return nil, fmt.Errorf("OIDC issuer required")
	}
	if cfg.JWKSURL == "" {
		cfg.JWKSURL = strings.TrimRight(cfg.Issuer, "/") + "/.well-known/jwks.json"
	}
	if cfg.ClaimOrg == "" {
		cfg.ClaimOrg = "org"
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 10 * time.Second}
	}
	if cfg.Audience == "" && os.Getenv("REHEARSAL_OIDC_ALLOW_NO_AUD") != "1" {
		cfg.RequireAudience = true
		// fail closed: production must set audience
		return nil, fmt.Errorf("REHEARSAL_OIDC_AUDIENCE required (or REHEARSAL_OIDC_ALLOW_NO_AUD=1 for tests only)")
	}
	if cfg.Audience != "" {
		cfg.RequireAudience = true
	}
	return &OIDCVerifier{cfg: cfg, keys: map[string]*rsa.PublicKey{}}, nil
}

// Authenticate validates Bearer JWT via JWKS + golang-jwt (RS256, iss, aud, exp, sub).
func (o *OIDCVerifier) Authenticate(r *http.Request) (Principal, error) {
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, "Bearer ") {
		return Principal{}, fmt.Errorf("missing bearer")
	}
	tokStr := strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
	parts := strings.Split(tokStr, ".")
	if len(parts) != 3 {
		return Principal{}, fmt.Errorf("invalid jwt")
	}

	if err := o.ensureKeys(r.Context()); err != nil {
		return Principal{}, err
	}

	parser := jwt.NewParser(
		jwt.WithValidMethods([]string{jwt.SigningMethodRS256.Alg()}),
		jwt.WithIssuer(o.cfg.Issuer),
		jwt.WithExpirationRequired(),
		jwt.WithIssuedAt(),
	)
	if o.cfg.RequireAudience && o.cfg.Audience != "" {
		parser = jwt.NewParser(
			jwt.WithValidMethods([]string{jwt.SigningMethodRS256.Alg()}),
			jwt.WithIssuer(o.cfg.Issuer),
			jwt.WithAudience(o.cfg.Audience),
			jwt.WithExpirationRequired(),
			jwt.WithIssuedAt(),
		)
	}

	claims := jwt.MapClaims{}
	token, err := parser.ParseWithClaims(tokStr, claims, func(t *jwt.Token) (any, error) {
		kid, _ := t.Header["kid"].(string)
		o.mu.RLock()
		pub := o.keys[kid]
		if pub == nil && kid == "" {
			// single-key JWKS
			for _, k := range o.keys {
				pub = k
				break
			}
		}
		o.mu.RUnlock()
		if pub == nil {
			_ = o.refreshKeys(r.Context())
			o.mu.RLock()
			pub = o.keys[kid]
			o.mu.RUnlock()
		}
		if pub == nil {
			return nil, fmt.Errorf("unknown kid %q", kid)
		}
		return pub, nil
	})
	if err != nil {
		return Principal{}, fmt.Errorf("jwt: %w", err)
	}
	if !token.Valid {
		return Principal{}, fmt.Errorf("jwt invalid")
	}

	// Required claims
	sub, _ := claims["sub"].(string)
	if strings.TrimSpace(sub) == "" {
		return Principal{}, fmt.Errorf("jwt missing sub")
	}
	if _, ok := claims["exp"]; !ok {
		return Principal{}, fmt.Errorf("jwt missing exp")
	}
	if iss, _ := claims["iss"].(string); iss == "" {
		return Principal{}, fmt.Errorf("jwt missing iss")
	}
	// nbf handled by WithIssuedAt / default leeway if present in library via registered claims;
	// manually reject future nbf
	if nbf, ok := claims["nbf"].(float64); ok && time.Now().Unix() < int64(nbf)-60 {
		return Principal{}, fmt.Errorf("jwt not valid yet (nbf)")
	}

	email, _ := claims["email"].(string)
	org, _ := claims[o.cfg.ClaimOrg].(string)
	if org == "" {
		org, _ = claims["https://rehearsal.io/org"].(string)
	}
	if org == "" {
		return Principal{}, fmt.Errorf("jwt missing org claim (%s)", o.cfg.ClaimOrg)
	}
	roles := extractRoles(claims)
	if len(roles) == 0 {
		roles = []string{"viewer"}
	}
	return Principal{ID: sub, Email: email, Issuer: o.cfg.Issuer, Roles: roles, Org: org}, nil
}

func extractRoles(claims map[string]any) []string {
	if arr, ok := claims["roles"].([]any); ok {
		var out []string
		for _, x := range arr {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	if ra, ok := claims["realm_access"].(map[string]any); ok {
		if arr, ok := ra["roles"].([]any); ok {
			var out []string
			for _, x := range arr {
				if s, ok := x.(string); ok {
					out = append(out, s)
				}
			}
			return out
		}
	}
	return nil
}

func (o *OIDCVerifier) ensureKeys(ctx context.Context) error {
	o.mu.RLock()
	stale := time.Since(o.fetched) > 10*time.Minute || len(o.keys) == 0
	o.mu.RUnlock()
	if !stale {
		return nil
	}
	return o.refreshKeys(ctx)
}

func (o *OIDCVerifier) refreshKeys(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, o.cfg.JWKSURL, nil)
	if err != nil {
		return err
	}
	resp, err := o.cfg.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("jwks fetch %d", resp.StatusCode)
	}
	var doc struct {
		Keys []jwk `json:"keys"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return err
	}
	keys := map[string]*rsa.PublicKey{}
	for _, k := range doc.Keys {
		if k.Kty != "RSA" || k.N == "" || k.E == "" {
			continue
		}
		pub, err := jwkToRSA(k)
		if err != nil {
			continue
		}
		kid := k.Kid
		if kid == "" {
			kid = "default"
		}
		keys[kid] = pub
	}
	if len(keys) == 0 {
		return fmt.Errorf("no RSA keys in JWKS")
	}
	o.mu.Lock()
	o.keys = keys
	o.fetched = time.Now()
	o.mu.Unlock()
	return nil
}

type jwk struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	N   string `json:"n"`
	E   string `json:"e"`
}

func jwkToRSA(k jwk) (*rsa.PublicKey, error) {
	nb, err := base64.RawURLEncoding.DecodeString(k.N)
	if err != nil {
		return nil, err
	}
	eb, err := base64.RawURLEncoding.DecodeString(k.E)
	if err != nil {
		return nil, err
	}
	var e int
	for _, b := range eb {
		e = e<<8 + int(b)
	}
	return &rsa.PublicKey{N: new(big.Int).SetBytes(nb), E: e}, nil
}
