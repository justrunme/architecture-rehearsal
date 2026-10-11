package operator_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/justrunme/architecture-rehearsal/internal/operator"
	"github.com/justrunme/architecture-rehearsal/internal/run"
)

func TestEnsureRunIdempotent200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "r1", "spec": map[string]any{"baselineRef": "b", "changeRef": "c"},
			"status": map[string]any{"phase": "Pending"},
		})
	}))
	defer srv.Close()
	cp := &operator.ControlPlaneClient{BaseURL: srv.URL, Token: "x", HTTP: srv.Client()}
	res, err := cp.EnsureRun(run.NewRun("r1", "ik", run.Spec{BaselineRef: "b", ChangeRef: "c"}))
	if err != nil {
		t.Fatal(err)
	}
	if res.Created || res.Conflict {
		t.Fatalf("%+v", res)
	}
}

func TestCancelIdempotentOn404AndTerminal(t *testing.T) {
	var cancelPosts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/runs/missing"):
			w.WriteHeader(404)
			_, _ = w.Write([]byte("not found"))
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/runs/done"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "done", "status": map[string]any{"phase": "Completed"},
			})
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/runs/live"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "live", "status": map[string]any{"phase": "Rehearsing"},
			})
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/advance"):
			cancelPosts.Add(1)
			w.WriteHeader(200)
			_ = json.NewEncoder(w).Encode(map[string]any{"jobsCancelled": 0})
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	cp := &operator.ControlPlaneClient{BaseURL: srv.URL, Token: "x", HTTP: srv.Client()}
	if err := cp.Cancel("missing"); err != nil {
		t.Fatalf("404 cancel: %v", err)
	}
	if cancelPosts.Load() != 0 {
		t.Fatal("404 must not POST cancel")
	}
	if err := cp.Cancel("done"); err != nil {
		t.Fatalf("terminal cancel: %v", err)
	}
	if cancelPosts.Load() != 0 {
		t.Fatal("terminal must not POST cancel")
	}
	if err := cp.Cancel("live"); err != nil {
		t.Fatalf("live cancel: %v", err)
	}
	if cancelPosts.Load() != 1 {
		t.Fatalf("live cancel posts=%d", cancelPosts.Load())
	}
}

func TestHTTPStatusErrorPermanent(t *testing.T) {
	err400 := &operator.HTTPStatusError{Op: "create run", StatusCode: 400, Body: "bad"}
	err503 := &operator.HTTPStatusError{Op: "create run", StatusCode: 503, Body: "no"}
	if !err400.Permanent() || !operator.IsPermanent(err400) {
		t.Fatal("400 must be permanent")
	}
	if err503.Permanent() || operator.IsPermanent(err503) {
		t.Fatal("503 must be transient")
	}
	if !operator.IsNotFound(&operator.HTTPStatusError{StatusCode: 404}) {
		t.Fatal("404 IsNotFound")
	}
}
