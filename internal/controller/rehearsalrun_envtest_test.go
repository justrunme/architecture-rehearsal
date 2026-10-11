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
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/justrunme/architecture-rehearsal/api/v1beta1"
	"github.com/justrunme/architecture-rehearsal/internal/controller"
	"github.com/justrunme/architecture-rehearsal/internal/operator"
)

// envtest-style suite using the controller-runtime fake client + httptest control plane.
// Exercises finalizer lifecycle and permanent vs transient error classification.

func reconcileUntil(t *testing.T, rec *controller.RehearsalRunReconciler, req ctrl.Request, steps int) ctrl.Result {
	t.Helper()
	var res ctrl.Result
	var err error
	for i := 0; i < steps; i++ {
		res, err = rec.Reconcile(context.Background(), req)
		if err != nil {
			t.Fatalf("reconcile step %d: %v", i, err)
		}
	}
	return res
}

func TestEnvtestFinalizerCancelOnDelete(t *testing.T) {
	var cancels atomic.Int32
	var gets atomic.Int32
	phase := "Pending"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/runs":
			body, _ := io.ReadAll(r.Body)
			var req map[string]any
			_ = json.Unmarshal(body, &req)
			w.WriteHeader(201)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": req["id"],
				"spec": map[string]any{
					"baselineRef": req["baselineRef"], "changeRef": req["changeRef"],
				},
				"status": map[string]any{"phase": phase},
			})
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/advance"):
			body, _ := io.ReadAll(r.Body)
			var req map[string]any
			_ = json.Unmarshal(body, &req)
			if req["action"] == "cancel" {
				cancels.Add(1)
				phase = "Cancelled"
				_ = json.NewEncoder(w).Encode(map[string]any{
					"run": map[string]any{"id": "x", "status": map[string]any{"phase": "Cancelled"}},
					"jobsCancelled": 1,
				})
				return
			}
			w.WriteHeader(202)
			_ = json.NewEncoder(w).Encode(map[string]any{"jobId": "job-1", "status": "queued"})
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/runs/"):
			gets.Add(1)
			id := strings.TrimPrefix(r.URL.Path, "/v1/runs/")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": id,
				"spec": map[string]any{"baselineRef": "b.json", "changeRef": "c.json"},
				"status": map[string]any{"phase": phase},
			})
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()

	s := scheme(t)
	recFake := record.NewFakeRecorder(16)
	cr := &v1beta1.RehearsalRun{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "default", Generation: 1, UID: "aaaaaaaa-bbbb-cccc"},
		Spec:       v1beta1.RehearsalRunSpec{BaselineRef: "b.json", ChangeRef: "c.json"},
	}
	cl := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(cr).WithObjects(cr).Build()
	rec := &controller.RehearsalRunReconciler{
		Client: cl, Scheme: s, Recorder: recFake,
		APIBase: srv.URL, Token: "tok",
		ClientFactory: func(base, token string) *operator.ControlPlaneClient {
			return &operator.ControlPlaneClient{BaseURL: base, Token: token, HTTP: srv.Client()}
		},
	}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "demo", Namespace: "default"}}

	// Step 1: add finalizer; step 2: ensure+advance
	_ = reconcileUntil(t, rec, req, 2)
	var got v1beta1.RehearsalRun
	if err := cl.Get(context.Background(), req.NamespacedName, &got); err != nil {
		t.Fatal(err)
	}
	if !controllerutil.ContainsFinalizer(&got, controller.FinalizerCancelRun) {
		t.Fatalf("finalizer missing: %v", got.Finalizers)
	}
	if got.Status.ControlPlaneRunID == "" {
		t.Fatal("expected controlPlaneRunId")
	}

	// Mark for deletion
	now := metav1.Now()
	got.DeletionTimestamp = &now
	got.Finalizers = []string{controller.FinalizerCancelRun}
	if err := cl.Update(context.Background(), &got); err != nil {
		// fake client may require Delete to set DeletionTimestamp
		t.Logf("update with deletionTimestamp: %v — using Delete", err)
	}
	// Ensure deletionTimestamp via Delete (finalizer keeps object)
	if err := cl.Delete(context.Background(), &got); err != nil {
		t.Fatal(err)
	}

	res, err := rec.Reconcile(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if res.Requeue || res.RequeueAfter > 0 {
		t.Fatalf("unexpected requeue after delete: %+v", res)
	}
	if cancels.Load() != 1 {
		t.Fatalf("cancel calls=%d want 1", cancels.Load())
	}

	err = cl.Get(context.Background(), req.NamespacedName, &got)
	if err == nil {
		if controllerutil.ContainsFinalizer(&got, controller.FinalizerCancelRun) {
			t.Fatal("finalizer should be removed")
		}
	}
	// Second delete reconcile must be idempotent (no extra cancel if already gone / terminal)
	phase = "Cancelled"
	_, _ = rec.Reconcile(context.Background(), req)
	if cancels.Load() > 1 {
		// Cancel client skips terminal runs after GetRun — acceptable if 1 total
		t.Logf("cancels=%d (idempotent path)", cancels.Load())
	}
	if cancels.Load() != 1 {
		t.Fatalf("cancel must stay idempotent, got %d", cancels.Load())
	}
}

func TestEnvtestFinalizerSkipsCancelForTerminalRun(t *testing.T) {
	var cancels atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/advance"):
			body, _ := io.ReadAll(r.Body)
			var req map[string]any
			_ = json.Unmarshal(body, &req)
			if req["action"] == "cancel" {
				cancels.Add(1)
			}
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{}`))
		case r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "done",
				"spec": map[string]any{"baselineRef": "b.json", "changeRef": "c.json"},
				"status": map[string]any{"phase": "Completed"},
			})
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()

	s := scheme(t)
	cr := &v1beta1.RehearsalRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "done",
			Namespace:  "default",
			Generation: 1,
			UID:        "bbbbbbbb-cccc-dddd",
			Finalizers: []string{controller.FinalizerCancelRun},
		},
		Spec: v1beta1.RehearsalRunSpec{BaselineRef: "b.json", ChangeRef: "c.json"},
		Status: v1beta1.RehearsalRunStatus{
			ControlPlaneRunID: "default-done-bbbbbbbb-g1",
			Phase:             "Completed",
		},
	}
	cl := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(cr).WithObjects(cr).Build()
	rec := &controller.RehearsalRunReconciler{
		Client: cl, Scheme: s, Recorder: record.NewFakeRecorder(8),
		APIBase: srv.URL, Token: "tok",
		ClientFactory: func(base, token string) *operator.ControlPlaneClient {
			return &operator.ControlPlaneClient{BaseURL: base, Token: token, HTTP: srv.Client()}
		},
	}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "done", Namespace: "default"}}
	if err := cl.Delete(context.Background(), cr); err != nil {
		t.Fatal(err)
	}
	if _, err := rec.Reconcile(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if cancels.Load() != 0 {
		t.Fatalf("terminal run must not call cancel API, got %d", cancels.Load())
	}
}

func TestEnvtestPermanentErrorNoRequeue(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/runs":
			w.WriteHeader(200)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "x",
				"spec": map[string]any{
					"baselineRef": "b", "changeRef": "old",
					"scenarios": []string{"old"},
				},
				"status": map[string]any{"phase": "Pending"},
			})
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()

	s := scheme(t)
	recFake := record.NewFakeRecorder(8)
	cr := &v1beta1.RehearsalRun{
		ObjectMeta: metav1.ObjectMeta{Name: "mismatch", Namespace: "ns", Generation: 1, UID: "uid-1"},
		Spec:       v1beta1.RehearsalRunSpec{BaselineRef: "b", ChangeRef: "c", Scenarios: []string{"new"}},
	}
	cl := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(cr).WithObjects(cr).Build()
	rec := &controller.RehearsalRunReconciler{
		Client: cl, Scheme: s, Recorder: recFake,
		APIBase: srv.URL, Token: "tok",
		ClientFactory: func(base, token string) *operator.ControlPlaneClient {
			return &operator.ControlPlaneClient{BaseURL: base, Token: token, HTTP: srv.Client()}
		},
	}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "mismatch", Namespace: "ns"}}
	_ = reconcileUntil(t, rec, req, 1) // finalizer
	res, err := rec.Reconcile(context.Background(), req)
	if err != nil {
		t.Fatalf("permanent must return nil err, got %v", err)
	}
	if res.Requeue || res.RequeueAfter != 0 {
		t.Fatalf("permanent must not requeue: %+v", res)
	}
	// Reconcile again — still no error / no requeue (no hot-loop)
	res2, err := rec.Reconcile(context.Background(), req)
	if err != nil || res2.RequeueAfter != 0 {
		t.Fatalf("second reconcile: res=%+v err=%v", res2, err)
	}

	var got v1beta1.RehearsalRun
	_ = cl.Get(context.Background(), req.NamespacedName, &got)
	failed := false
	for _, c := range got.Status.Conditions {
		if c.Type == v1beta1.ConditionFailed && c.Status == metav1.ConditionTrue && c.Reason == "SpecConflict" {
			failed = true
		}
	}
	if !failed {
		t.Fatalf("want Failed SpecConflict, status=%+v", got.Status)
	}
	foundEvent := false
	deadline := time.After(200 * time.Millisecond)
drain:
	for {
		select {
		case e := <-recFake.Events:
			if strings.Contains(e, "SpecConflict") {
				foundEvent = true
				break drain
			}
		case <-deadline:
			break drain
		}
	}
	if !foundEvent {
		t.Fatal("expected SpecConflict event")
	}
}

func TestEnvtestTransientErrorReturned(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/v1/runs" {
			w.WriteHeader(503)
			_, _ = w.Write([]byte(`unavailable`))
			return
		}
		w.WriteHeader(404)
	}))
	defer srv.Close()

	s := scheme(t)
	cr := &v1beta1.RehearsalRun{
		ObjectMeta: metav1.ObjectMeta{Name: "tmp", Namespace: "ns", Generation: 1, UID: "uid-2"},
		Spec:       v1beta1.RehearsalRunSpec{BaselineRef: "b", ChangeRef: "c"},
	}
	cl := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(cr).WithObjects(cr).Build()
	rec := &controller.RehearsalRunReconciler{
		Client: cl, Scheme: s, Recorder: record.NewFakeRecorder(4),
		APIBase: srv.URL, Token: "tok",
		ClientFactory: func(base, token string) *operator.ControlPlaneClient {
			return &operator.ControlPlaneClient{BaseURL: base, Token: token, HTTP: srv.Client()}
		},
	}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "tmp", Namespace: "ns"}}
	_ = reconcileUntil(t, rec, req, 1)
	_, err := rec.Reconcile(context.Background(), req)
	if err == nil {
		t.Fatal("want transient error returned for backoff")
	}
	if controller.IsPermanent(err) {
		t.Fatalf("503 must be transient, got permanent: %v", err)
	}
}

func TestEnvtestClientFourXXPermanent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		_, _ = w.Write([]byte(`bad request`))
	}))
	defer srv.Close()
	s := scheme(t)
	cr := &v1beta1.RehearsalRun{
		ObjectMeta: metav1.ObjectMeta{Name: "bad", Namespace: "ns", Generation: 1, UID: "uid-3", Finalizers: []string{controller.FinalizerCancelRun}},
		Spec:       v1beta1.RehearsalRunSpec{BaselineRef: "b", ChangeRef: "c"},
	}
	cl := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(cr).WithObjects(cr).Build()
	rec := &controller.RehearsalRunReconciler{
		Client: cl, Scheme: s, Recorder: record.NewFakeRecorder(4),
		APIBase: srv.URL, Token: "tok",
		ClientFactory: func(base, token string) *operator.ControlPlaneClient {
			return &operator.ControlPlaneClient{BaseURL: base, Token: token, HTTP: srv.Client()}
		},
	}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "bad", Namespace: "ns"}}
	res, err := rec.Reconcile(context.Background(), req)
	if err != nil {
		t.Fatalf("permanent 400 must not return err (no backoff), got %v", err)
	}
	if res.RequeueAfter != 0 {
		t.Fatalf("no requeue for permanent: %+v", res)
	}
}

func TestEnvtestInconclusiveNotReady(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/runs":
			body, _ := io.ReadAll(r.Body)
			var req map[string]any
			_ = json.Unmarshal(body, &req)
			w.WriteHeader(201)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": req["id"],
				"spec": map[string]any{"baselineRef": "b.json", "changeRef": "c.json"},
				"status": map[string]any{"phase": "Inconclusive", "message": "coverage gap"},
			})
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/advance"):
			// existing terminal — advance may still be attempted depending on path
			w.WriteHeader(200)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "x",
				"spec": map[string]any{"baselineRef": "b.json", "changeRef": "c.json"},
				"status": map[string]any{"phase": "Inconclusive", "message": "coverage gap"},
			})
		case r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "x",
				"spec": map[string]any{"baselineRef": "b.json", "changeRef": "c.json"},
				"status": map[string]any{"phase": "Inconclusive", "message": "coverage gap"},
			})
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()

	s := scheme(t)
	cr := &v1beta1.RehearsalRun{
		ObjectMeta: metav1.ObjectMeta{Name: "inc", Namespace: "default", Generation: 1, UID: "cccccccc-dddd-eeee"},
		Spec:       v1beta1.RehearsalRunSpec{BaselineRef: "b.json", ChangeRef: "c.json"},
	}
	cl := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(cr).WithObjects(cr).Build()
	rec := &controller.RehearsalRunReconciler{
		Client: cl, Scheme: s, Recorder: record.NewFakeRecorder(8),
		APIBase: srv.URL, Token: "tok",
		ClientFactory: func(base, token string) *operator.ControlPlaneClient {
			return &operator.ControlPlaneClient{BaseURL: base, Token: token, HTTP: srv.Client()}
		},
	}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "inc", Namespace: "default"}}
	_ = reconcileUntil(t, rec, req, 2)
	var got v1beta1.RehearsalRun
	if err := cl.Get(context.Background(), req.NamespacedName, &got); err != nil {
		t.Fatal(err)
	}
	if got.Status.Phase != "Inconclusive" {
		t.Fatalf("phase=%q", got.Status.Phase)
	}
	readyTrue := false
	inconclusiveReason := false
	for _, c := range got.Status.Conditions {
		if c.Type == v1beta1.ConditionReady {
			if c.Status == metav1.ConditionTrue {
				readyTrue = true
			}
			if c.Reason == "Inconclusive" && c.Status == metav1.ConditionFalse {
				inconclusiveReason = true
			}
		}
	}
	if readyTrue {
		t.Fatal("Inconclusive must not set Ready=True")
	}
	if !inconclusiveReason {
		t.Fatalf("want Ready=False reason=Inconclusive, conditions=%+v", got.Status.Conditions)
	}
}

// Ensure client interface still compiles against fake client.
var _ client.StatusClient = (client.Client)(nil)
