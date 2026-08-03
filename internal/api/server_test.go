package api_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/justrunme/architecture-rehearsal/internal/api"
	"github.com/justrunme/architecture-rehearsal/internal/authn"
	"github.com/justrunme/architecture-rehearsal/internal/persist"
)

func auth(req *http.Request, tok string) {
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
}

func TestCreateAndGetRun(t *testing.T) {
	// NewServer uses insecure local-dev for unit tests
	s := api.NewServer()
	h := s.Handler()

	body := []byte(`{"id":"r1","idempotencyKey":"ik1","clusterName":"c","baselineRef":"b.json","changeRef":"c.json"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/runs", bytes.NewReader(body))
	auth(req, "local-dev")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != 201 {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}

	req2 := httptest.NewRequest(http.MethodPost, "/v1/runs", bytes.NewReader(body))
	auth(req2, "local-dev")
	rr2 := httptest.NewRecorder()
	h.ServeHTTP(rr2, req2)
	if rr2.Code != 200 {
		t.Fatalf("idempotent code=%d body=%s", rr2.Code, rr2.Body.String())
	}

	req3 := httptest.NewRequest(http.MethodGet, "/v1/runs/r1", nil)
	auth(req3, "local-dev")
	rr3 := httptest.NewRecorder()
	h.ServeHTTP(rr3, req3)
	if rr3.Code != 200 {
		t.Fatal(rr3.Body.String())
	}
}

func TestUnauthorized(t *testing.T) {
	s := api.NewServer()
	req := httptest.NewRequest(http.MethodGet, "/v1/runs", nil)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != 401 {
		t.Fatalf("%d", rr.Code)
	}
}

func TestHardcodedTokensRemoved(t *testing.T) {
	os.Setenv("REHEARSAL_API_TOKEN", "only-secret-token")
	os.Unsetenv("REHEARSAL_ALLOW_INSECURE_DEV")
	os.Unsetenv("REHEARSAL_API_TOKENS")
	a, err := authn.FromEnv(authn.Config{RequireToken: true})
	if err != nil {
		t.Fatal(err)
	}
	s := api.NewServerWith(api.Options{AuthN: a})
	h := s.Handler()

	// create with valid token
	body := []byte(`{"id":"victim-run","baselineRef":"b.json","changeRef":"c.json"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/runs", bytes.NewReader(body))
	auth(req, "only-secret-token")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != 201 {
		t.Fatalf("create %d %s", rr.Code, rr.Body.String())
	}

	// known viewer-token must fail
	for _, tok := range []string{"viewer-token", "ci", "local-dev"} {
		req := httptest.NewRequest(http.MethodGet, "/v1/runs/victim-run", nil)
		auth(req, tok)
		// even with X-Org spoof
		req.Header.Set("X-Org", "default")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code == 200 {
			t.Fatalf("token %q must not access runs, got 200", tok)
		}
	}
}

func TestCrossTenantIsolation(t *testing.T) {
	tokens := map[string]authn.Principal{
		"tok-victim":  {ID: "v", Roles: []string{"operator"}, Org: "victim"},
		"tok-attacker": {ID: "a", Roles: []string{"operator"}, Org: "attacker"},
	}
	s := api.NewServerWith(api.Options{AuthN: &authn.StaticToken{Tokens: tokens}})
	h := s.Handler()

	body := []byte(`{"id":"secret-run","baselineRef":"b.json","changeRef":"c.json","org":"should-be-ignored"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/runs", bytes.NewReader(body))
	auth(req, "tok-victim")
	req.Header.Set("X-Org", "attacker") // must not rebind principal org
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != 201 {
		t.Fatalf("create %d %s", rr.Code, rr.Body.String())
	}
	var created map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &created)
	labels, _ := created["labels"].(map[string]any)
	if labels["org"] != "victim" {
		t.Fatalf("org must be principal org victim, got %v", labels)
	}

	// attacker cannot read
	req2 := httptest.NewRequest(http.MethodGet, "/v1/runs/secret-run", nil)
	auth(req2, "tok-attacker")
	req2.Header.Set("X-Org", "victim") // spoof
	rr2 := httptest.NewRecorder()
	h.ServeHTTP(rr2, req2)
	if rr2.Code != 404 {
		t.Fatalf("cross-tenant read want 404 got %d body=%s", rr2.Code, rr2.Body.String())
	}

	// attacker advance/evidence denied
	for _, path := range []string{
		"/v1/runs/secret-run/evidence",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		auth(req, "tok-attacker")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != 404 {
			t.Fatalf("%s want 404 got %d", path, rec.Code)
		}
	}
	req3 := httptest.NewRequest(http.MethodPost, "/v1/runs/secret-run/advance", bytes.NewReader([]byte(`{"action":"cancel"}`)))
	auth(req3, "tok-attacker")
	rr3 := httptest.NewRecorder()
	h.ServeHTTP(rr3, req3)
	if rr3.Code != 404 {
		t.Fatalf("advance want 404 got %d", rr3.Code)
	}

	// victim can read
	req4 := httptest.NewRequest(http.MethodGet, "/v1/runs/secret-run", nil)
	auth(req4, "tok-victim")
	rr4 := httptest.NewRecorder()
	h.ServeHTTP(rr4, req4)
	if rr4.Code != 200 {
		t.Fatalf("owner read %d", rr4.Code)
	}
}

func TestWorkDirSandbox(t *testing.T) {
	root := t.TempDir()
	s := api.NewServerWith(api.Options{
		AuthN:       authn.Default(),
		WorkDirRoot: root,
	})
	h := s.Handler()
	// absolute path outside root rejected on create
	body := []byte(`{"id":"r2","baselineRef":"/etc/passwd","changeRef":"c.json"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/runs", bytes.NewReader(body))
	auth(req, "local-dev")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != 400 {
		t.Fatalf("want 400 for path escape, got %d %s", rr.Code, rr.Body.String())
	}
	// relative ok
	_ = os.WriteFile(filepath.Join(root, "b.json"), []byte(`{}`), 0o644)
	body = []byte(`{"id":"r3","baselineRef":"b.json","changeRef":"c.json"}`)
	req = httptest.NewRequest(http.MethodPost, "/v1/runs", bytes.NewReader(body))
	auth(req, "local-dev")
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != 201 {
		t.Fatalf("relative path %d %s", rr.Code, rr.Body.String())
	}
}

func TestOIDCStubRejected(t *testing.T) {
	os.Setenv("REHEARSAL_API_TOKEN", "real-token")
	os.Setenv("REHEARSAL_OIDC_ISSUER", "https://issuer.example")
	os.Unsetenv("REHEARSAL_ALLOW_INSECURE_DEV")
	a, err := authn.FromEnv(authn.Config{RequireToken: true})
	if err != nil {
		t.Fatal(err)
	}
	// fake JWT
	req := httptest.NewRequest(http.MethodGet, "/v1/runs", nil)
	req.Header.Set("Authorization", "Bearer aaa.bbb.ccc")
	_, err = a.Authenticate(req)
	if err == nil {
		t.Fatal("unsigned JWT must be rejected")
	}
	os.Unsetenv("REHEARSAL_OIDC_ISSUER")
}

func TestFromEnvRequiresToken(t *testing.T) {
	os.Unsetenv("REHEARSAL_API_TOKEN")
	os.Unsetenv("REHEARSAL_API_TOKEN_FILE")
	os.Unsetenv("REHEARSAL_API_TOKENS")
	os.Unsetenv("REHEARSAL_ALLOW_INSECURE_DEV")
	_, err := authn.FromEnv(authn.Config{RequireToken: true})
	if err == nil {
		t.Fatal("expected error without token")
	}
}

func TestAsyncAdvanceEnqueue(t *testing.T) {
	s := api.NewServerWith(api.Options{
		AuthN: authn.Default(),
		Async: true,
	})
	h := s.Handler()
	body := []byte(`{"id":"async-r1","baselineRef":"b.json","changeRef":"c.json"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/runs", bytes.NewReader(body))
	auth(req, "local-dev")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != 201 {
		t.Fatalf("create %d %s", rr.Code, rr.Body.String())
	}

	req2 := httptest.NewRequest(http.MethodPost, "/v1/runs/async-r1/advance", bytes.NewReader([]byte(`{}`)))
	auth(req2, "local-dev")
	rr2 := httptest.NewRecorder()
	h.ServeHTTP(rr2, req2)
	if rr2.Code != 202 {
		t.Fatalf("async advance want 202 got %d %s", rr2.Code, rr2.Body.String())
	}
	var out map[string]any
	_ = json.Unmarshal(rr2.Body.Bytes(), &out)
	if out["jobId"] == nil || out["status"] != "queued" {
		t.Fatalf("%v", out)
	}

	// metrics endpoint
	req3 := httptest.NewRequest(http.MethodGet, "/v1/metrics", nil)
	rr3 := httptest.NewRecorder()
	h.ServeHTTP(rr3, req3)
	if rr3.Code != 200 {
		t.Fatalf("metrics %d", rr3.Code)
	}
	if !bytes.Contains(rr3.Body.Bytes(), []byte("rehearsal_jobs")) {
		t.Fatalf("metrics body: %s", rr3.Body.String())
	}
}

func TestSQLBackendRuns(t *testing.T) {
	dir := t.TempDir()
	st, err := persist.Open(filepath.Join(dir, "api.db"), filepath.Join(dir, "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	s := api.NewServerWith(api.Options{
		AuthN:   authn.Default(),
		Backend: &api.SQLBackend{S: st},
	})
	h := s.Handler()
	body := []byte(`{"id":"sql-r1","idempotencyKey":"sql-ik","baselineRef":"b.json","changeRef":"c.json"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/runs", bytes.NewReader(body))
	auth(req, "local-dev")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != 201 {
		t.Fatalf("create %d %s", rr.Code, rr.Body.String())
	}
	// idempotent
	req2 := httptest.NewRequest(http.MethodPost, "/v1/runs", bytes.NewReader(body))
	auth(req2, "local-dev")
	rr2 := httptest.NewRecorder()
	h.ServeHTTP(rr2, req2)
	if rr2.Code != 200 {
		t.Fatalf("idem %d %s", rr2.Code, rr2.Body.String())
	}
	req3 := httptest.NewRequest(http.MethodGet, "/v1/runs/sql-r1", nil)
	auth(req3, "local-dev")
	rr3 := httptest.NewRecorder()
	h.ServeHTTP(rr3, req3)
	if rr3.Code != 200 {
		t.Fatalf("get %d", rr3.Code)
	}
}
