package operator_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
