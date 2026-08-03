package controller_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/justrunme/architecture-rehearsal/api/v1beta1"
	"github.com/justrunme/architecture-rehearsal/internal/controller"
	"github.com/justrunme/architecture-rehearsal/internal/operator"
	"github.com/justrunme/architecture-rehearsal/internal/run"
)

func TestSpecDigestChangesWithRefs(t *testing.T) {
	a, err := controller.SpecDigest(v1beta1.RehearsalRunSpec{BaselineRef: "b1", ChangeRef: "c1"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := controller.SpecDigest(v1beta1.RehearsalRunSpec{BaselineRef: "b1", ChangeRef: "c2"})
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("digest must change when changeRef changes")
	}
	a2, _ := controller.SpecDigest(v1beta1.RehearsalRunSpec{BaselineRef: "b1", ChangeRef: "c1"})
	if a != a2 {
		t.Fatal("digest must be stable")
	}
}

func TestEnsureRunConflictNotSilentSuccess(t *testing.T) {
	// First create succeeds; second create same id returns 409 + existing with different changeRef.
	var creates int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/runs":
			creates++
			body, _ := io.ReadAll(r.Body)
			var req map[string]any
			_ = json.Unmarshal(body, &req)
			if creates == 1 {
				w.WriteHeader(201)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"id": req["id"], "spec": map[string]any{"baselineRef": req["baselineRef"], "changeRef": req["changeRef"]},
					"status": map[string]any{"phase": "Pending"},
				})
				return
			}
			w.WriteHeader(409)
			_, _ = w.Write([]byte(`conflict`))
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/runs/"):
			// existing run has OLD change ref
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "r1",
				"spec": map[string]any{
					"baselineRef": "b.json",
					"changeRef":   "old-change.json",
				},
				"status": map[string]any{"phase": "Completed"},
			})
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()

	cp := &operator.ControlPlaneClient{BaseURL: srv.URL, Token: "t", HTTP: srv.Client()}
	rr := run.NewRun("r1", "r1", run.Spec{BaselineRef: "b.json", ChangeRef: "new-change.json"})
	// first create
	res, err := cp.EnsureRun(rr)
	if err != nil || !res.Created {
		t.Fatalf("first create: %v %#v", err, res)
	}
	// second — conflict with different refs
	res2, err := cp.EnsureRun(rr)
	if err != nil {
		t.Fatal(err)
	}
	if !res2.Conflict {
		t.Fatal("want Conflict=true on 409")
	}
	if res2.Run == nil || res2.Run.Spec.ChangeRef != "old-change.json" {
		t.Fatalf("existing run: %#v", res2.Run)
	}
}

func TestAdvanceReturnsJobID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(404)
			return
		}
		w.WriteHeader(202)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"runId": "r1", "jobId": "job-xyz", "status": "queued",
		})
	}))
	defer srv.Close()
	cp := &operator.ControlPlaneClient{BaseURL: srv.URL, Token: "t", HTTP: srv.Client()}
	adv, err := cp.Advance("r1", true)
	if err != nil {
		t.Fatal(err)
	}
	if adv.JobID != "job-xyz" {
		t.Fatalf("jobId=%q", adv.JobID)
	}
}

func TestControlPlaneURLNotOnType(t *testing.T) {
	// Compile-time / schema: ControlPlaneURL must not exist on Spec.
	raw, _ := json.Marshal(v1beta1.RehearsalRunSpec{BaselineRef: "b", ChangeRef: "c"})
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	if _, ok := m["controlPlaneURL"]; ok {
		t.Fatal("controlPlaneURL must not be serialized on Spec")
	}
}
