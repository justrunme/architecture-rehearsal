package authn_test

import (
	"net/http"
	"os"
	"testing"

	"github.com/justrunme/architecture-rehearsal/internal/authn"
)

func TestNoBuiltInSharedTokens(t *testing.T) {
	os.Setenv("REHEARSAL_API_TOKEN", "prod-secret")
	os.Unsetenv("REHEARSAL_ALLOW_INSECURE_DEV")
	os.Unsetenv("REHEARSAL_API_TOKENS")
	a, err := authn.FromEnv(authn.Config{RequireToken: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"ci", "viewer-token", "local-dev"} {
		req, _ := http.NewRequest("GET", "/", nil)
		req.Header.Set("Authorization", "Bearer "+bad)
		if _, err := a.Authenticate(req); err == nil {
			t.Fatalf("%s must fail", bad)
		}
	}
	req, _ := http.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer prod-secret")
	req.Header.Set("X-Org", "evil")
	p, err := a.Authenticate(req)
	if err != nil {
		t.Fatal(err)
	}
	if p.Org == "evil" {
		t.Fatal("X-Org must not override principal org")
	}
}
