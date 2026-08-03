package authn_test

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/justrunme/architecture-rehearsal/internal/authn"
)

func TestOIDCStrictClaims(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	jwks := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := base64.RawURLEncoding.EncodeToString(key.N.Bytes())
		e := base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes())
		_ = json.NewEncoder(w).Encode(map[string]any{
			"keys": []map[string]string{{"kty": "RSA", "kid": "k1", "n": n, "e": e}},
		})
	}))
	defer jwks.Close()

	v, err := authn.NewOIDCVerifier(authn.OIDCConfig{
		Issuer: "https://issuer.example", Audience: "rehearsal-api", JWKSURL: jwks.URL,
		HTTPClient: jwks.Client(), ClaimOrg: "org",
	})
	if err != nil {
		t.Fatal(err)
	}

	mk := func(claims jwt.MapClaims) string {
		tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
		tok.Header["kid"] = "k1"
		s, err := tok.SignedString(key)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}

	// missing aud → fail
	bad := mk(jwt.MapClaims{
		"iss": "https://issuer.example", "sub": "u1", "org": "acme",
		"exp": float64(time.Now().Add(time.Hour).Unix()),
	})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+bad)
	if _, err := v.Authenticate(req); err == nil {
		t.Fatal("missing aud must fail")
	}

	// missing exp → fail
	bad2 := mk(jwt.MapClaims{
		"iss": "https://issuer.example", "sub": "u1", "org": "acme", "aud": "rehearsal-api",
	})
	req2 := httptest.NewRequest(http.MethodGet, "/", nil)
	req2.Header.Set("Authorization", "Bearer "+bad2)
	if _, err := v.Authenticate(req2); err == nil {
		t.Fatal("missing exp must fail")
	}

	// missing sub → fail
	bad3 := mk(jwt.MapClaims{
		"iss": "https://issuer.example", "org": "acme", "aud": "rehearsal-api",
		"exp": float64(time.Now().Add(time.Hour).Unix()),
	})
	req3 := httptest.NewRequest(http.MethodGet, "/", nil)
	req3.Header.Set("Authorization", "Bearer "+bad3)
	if _, err := v.Authenticate(req3); err == nil {
		t.Fatal("missing sub must fail")
	}

	// good
	good := mk(jwt.MapClaims{
		"iss": "https://issuer.example", "sub": "u1", "org": "acme", "aud": "rehearsal-api",
		"exp": float64(time.Now().Add(time.Hour).Unix()), "iat": float64(time.Now().Unix()),
		"roles": []any{"operator"},
	})
	req4 := httptest.NewRequest(http.MethodGet, "/", nil)
	req4.Header.Set("Authorization", "Bearer "+good)
	p, err := v.Authenticate(req4)
	if err != nil {
		t.Fatal(err)
	}
	if p.ID != "u1" || p.Org != "acme" {
		t.Fatalf("%+v", p)
	}

	// audience required at construction
	if _, err := authn.NewOIDCVerifier(authn.OIDCConfig{Issuer: "https://x", JWKSURL: jwks.URL}); err == nil {
		t.Fatal("audience required")
	}
}
