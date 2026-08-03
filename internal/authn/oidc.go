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

// OIDCConfig for JWKS-based JWT verification.
type OIDCConfig struct {
	Issuer   string
	Audience string
	JWKSURL  string
	// ClaimOrg is the JWT claim for organization (default: "org" or "https://rehearsal.io/org").
	ClaimOrg string
	// RoleClaim maps to roles (default "roles" or realm_access).
	HTTPClient *http.Client
}

// OIDCVerifier validates RS256 JWTs against a JWKS endpoint.
type OIDCVerifier struct {
	cfg    OIDCConfig
	mu     sync.RWMutex
	keys   map[string]*rsa.PublicKey // kid → key
	fetched time.Time
}

// NewOIDCVerifier creates a verifier. Does not fetch until first use.
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
	return &OIDCVerifier{cfg: cfg, keys: map[string]*rsa.PublicKey{}}, nil
}

// Authenticate validates Bearer JWT via JWKS.
func (o *OIDCVerifier) Authenticate(r *http.Request) (Principal, error) {
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, "Bearer ") {
		return Principal{}, fmt.Errorf("missing bearer")
	}
	tok := strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		return Principal{}, fmt.Errorf("invalid jwt")
	}
	hdrJSON, err := b64JSON(parts[0])
	if err != nil {
		return Principal{}, fmt.Errorf("jwt header: %w", err)
	}
	var hdr struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
		Typ string `json:"typ"`
	}
	if err := json.Unmarshal(hdrJSON, &hdr); err != nil {
		return Principal{}, err
	}
	if hdr.Alg != "RS256" {
		return Principal{}, fmt.Errorf("unsupported alg %s (need RS256)", hdr.Alg)
	}
	if err := o.ensureKeys(r.Context()); err != nil {
		return Principal{}, err
	}
	o.mu.RLock()
	pub := o.keys[hdr.Kid]
	o.mu.RUnlock()
	if pub == nil {
		// refresh once
		_ = o.refreshKeys(r.Context())
		o.mu.RLock()
		pub = o.keys[hdr.Kid]
		o.mu.RUnlock()
	}
	if pub == nil {
		return Principal{}, fmt.Errorf("unknown kid %q", hdr.Kid)
	}
	// Verify signature using crypto/rsa PSS? RS256 is PKCS1v15
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return Principal{}, err
	}
	signingInput := parts[0] + "." + parts[1]
	if err := verifyRS256(pub, []byte(signingInput), sig); err != nil {
		return Principal{}, fmt.Errorf("jwt signature: %w", err)
	}
	claimsJSON, err := b64JSON(parts[1])
	if err != nil {
		return Principal{}, err
	}
	var claims map[string]any
	if err := json.Unmarshal(claimsJSON, &claims); err != nil {
		return Principal{}, err
	}
	if iss, _ := claims["iss"].(string); iss != "" && iss != o.cfg.Issuer {
		return Principal{}, fmt.Errorf("issuer mismatch")
	}
	if o.cfg.Audience != "" {
		if !audOK(claims["aud"], o.cfg.Audience) {
			return Principal{}, fmt.Errorf("audience mismatch")
		}
	}
	if exp, ok := claims["exp"].(float64); ok && time.Now().Unix() > int64(exp) {
		return Principal{}, fmt.Errorf("token expired")
	}
	sub, _ := claims["sub"].(string)
	email, _ := claims["email"].(string)
	org, _ := claims[o.cfg.ClaimOrg].(string)
	if org == "" {
		org, _ = claims["https://rehearsal.io/org"].(string)
	}
	if org == "" {
		org = strings.TrimSpace(os.Getenv("REHEARSAL_API_ORG"))
	}
	if org == "" {
		org = "default"
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

func audOK(aud any, want string) bool {
	switch v := aud.(type) {
	case string:
		return v == want
	case []any:
		for _, x := range v {
			if s, ok := x.(string); ok && s == want {
				return true
			}
		}
	}
	return false
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
	Alg string `json:"alg"`
	Use string `json:"use"`
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

func b64JSON(s string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(s)
}

func verifyRS256(pub *rsa.PublicKey, signingInput, sig []byte) error {
	h := sha256Sum(signingInput)
	return rsa.VerifyPKCS1v15(pub, cryptoHashSHA256(), h, sig)
}
