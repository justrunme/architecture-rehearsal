package controller_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/justrunme/architecture-rehearsal/api/v1beta1"
	"github.com/justrunme/architecture-rehearsal/internal/controller"
	"github.com/justrunme/architecture-rehearsal/internal/operator"
	"github.com/justrunme/architecture-rehearsal/internal/run"
)

func scheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(s)
	_ = v1beta1.AddToScheme(s)
	return s
}

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

func TestControlPlaneURLNotOnType(t *testing.T) {
	raw, _ := json.Marshal(v1beta1.RehearsalRunSpec{BaselineRef: "b", ChangeRef: "c"})
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	if _, ok := m["controlPlaneURL"]; ok {
		t.Fatal("controlPlaneURL must not be serialized on Spec")
	}
}

func TestEnsureRunConflictNotSilentSuccess(t *testing.T) {
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
	res, err := cp.EnsureRun(rr)
	if err != nil || !res.Created {
		t.Fatalf("first create: %v %#v", err, res)
	}
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

func TestSetCondKeepsTimestampWithoutTransition(t *testing.T) {
	// exercise via reconcile status conditions stability: same message should keep time
	// Use package-level behavior through full reconcile twice.
	var advances atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/runs":
			w.WriteHeader(201)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "default-demo-g1", "spec": map[string]any{"baselineRef": "b.json", "changeRef": "c.json"},
				"status": map[string]any{"phase": "Pending"},
			})
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/advance"):
			advances.Add(1)
			w.WriteHeader(202)
			_ = json.NewEncoder(w).Encode(map[string]any{"jobId": "job-1", "status": "queued"})
		case r.Method == http.MethodGet:
			phase := "Pending"
			if advances.Load() > 0 {
				phase = "Completed"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "default-demo-g1",
				"spec": map[string]any{"baselineRef": "b.json", "changeRef": "c.json"},
				"status": map[string]any{"phase": phase, "decision": "approve", "message": "ok"},
				"labels": map[string]string{"chainBlob": "abc123"},
			})
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()

	s := scheme(t)
	cr := &v1beta1.RehearsalRun{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "default", Generation: 1},
		Spec:       v1beta1.RehearsalRunSpec{BaselineRef: "b.json", ChangeRef: "c.json"},
	}
	// Generation is not set by fake client automatically for Create — set ResourceVersion
	cl := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(cr).WithObjects(cr).Build()
	rec := &controller.RehearsalRunReconciler{
		Client:  cl,
		Scheme:  s,
		APIBase: srv.URL,
		Token:   "secret-token",
		ClientFactory: func(base, token string) *operator.ControlPlaneClient {
			return &operator.ControlPlaneClient{BaseURL: base, Token: token, HTTP: srv.Client()}
		},
	}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "demo", Namespace: "default"}}
	if _, err := rec.Reconcile(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	var got v1beta1.RehearsalRun
	if err := cl.Get(context.Background(), req.NamespacedName, &got); err != nil {
		t.Fatal(err)
	}
	if got.Status.JobID != "job-1" {
		t.Fatalf("jobId=%q", got.Status.JobID)
	}
	if got.Status.ObservedGeneration != 1 {
		t.Fatalf("observedGeneration=%d", got.Status.ObservedGeneration)
	}
	if got.Status.SpecDigest == "" {
		t.Fatal("specDigest empty")
	}
	if got.Status.EvidenceDigest != "abc123" {
		t.Fatalf("evidence=%q", got.Status.EvidenceDigest)
	}
	// Capture Accepted lastTransitionTime
	var acceptedTime metav1.Time
	for _, c := range got.Status.Conditions {
		if c.Type == v1beta1.ConditionAccepted {
			acceptedTime = c.LastTransitionTime
		}
	}
	if acceptedTime.IsZero() {
		t.Fatal("Accepted condition missing")
	}
	time.Sleep(5 * time.Millisecond)
	// Second reconcile: terminal, should not re-advance
	if _, err := rec.Reconcile(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if advances.Load() != 1 {
		t.Fatalf("advance calls want 1 got %d (no duplicate)", advances.Load())
	}
	_ = cl.Get(context.Background(), req.NamespacedName, &got)
	for _, c := range got.Status.Conditions {
		if c.Type == v1beta1.ConditionAccepted && !c.LastTransitionTime.Equal(&acceptedTime) {
			// Allowed only if condition content changed; Accepted should be stable
			if c.Status == metav1.ConditionTrue && c.Reason == "Accepted" {
				t.Fatal("Accepted lastTransitionTime changed without transition")
			}
		}
	}
}

func TestGenerationCreatesNewRunID(t *testing.T) {
	var createIDs []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/runs":
			body, _ := io.ReadAll(r.Body)
			var req map[string]any
			_ = json.Unmarshal(body, &req)
			id, _ := req["id"].(string)
			createIDs = append(createIDs, id)
			w.WriteHeader(201)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": id, "spec": map[string]any{"baselineRef": req["baselineRef"], "changeRef": req["changeRef"]},
				"status": map[string]any{"phase": "Pending"},
			})
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/advance"):
			w.WriteHeader(202)
			_ = json.NewEncoder(w).Encode(map[string]any{"jobId": "j2", "status": "queued"})
		case r.Method == http.MethodGet:
			id := strings.TrimPrefix(r.URL.Path, "/v1/runs/")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": id, "spec": map[string]any{"baselineRef": "b.json", "changeRef": "c2.json"},
				"status": map[string]any{"phase": "Completed"},
			})
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()

	s := scheme(t)
	cr := &v1beta1.RehearsalRun{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "ns", Generation: 2},
		Spec:       v1beta1.RehearsalRunSpec{BaselineRef: "b.json", ChangeRef: "c2.json"},
	}
	cl := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(cr).WithObjects(cr).Build()
	rec := &controller.RehearsalRunReconciler{
		Client: cl, Scheme: s, APIBase: srv.URL, Token: "tok",
		ClientFactory: func(base, token string) *operator.ControlPlaneClient {
			return &operator.ControlPlaneClient{BaseURL: base, Token: token, HTTP: srv.Client()}
		},
	}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "demo", Namespace: "ns"}}
	if _, err := rec.Reconcile(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if len(createIDs) != 1 || createIDs[0] != "ns-demo-g2" {
		t.Fatalf("create ids=%v", createIDs)
	}
	var got v1beta1.RehearsalRun
	_ = cl.Get(context.Background(), req.NamespacedName, &got)
	if got.Status.ControlPlaneRunID != "ns-demo-g2" {
		t.Fatalf("runId=%q", got.Status.ControlPlaneRunID)
	}
}

// Ensure fake client implements status writer.
var _ client.Client = (client.Client)(nil)
