package api_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/justrunme/architecture-rehearsal/internal/api"
)

func TestCreateAndGetRun(t *testing.T) {
	s := api.NewServer()
	h := s.Handler()

	body := []byte(`{"id":"r1","idempotencyKey":"ik1","clusterName":"c","baselineRef":"b.json","changeRef":"c.json","org":"default"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/runs", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer local-dev")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Org", "default")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != 201 {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}

	req2 := httptest.NewRequest(http.MethodPost, "/v1/runs", bytes.NewReader(body))
	req2.Header.Set("Authorization", "Bearer local-dev")
	req2.Header.Set("X-Org", "default")
	rr2 := httptest.NewRecorder()
	h.ServeHTTP(rr2, req2)
	if rr2.Code != 200 {
		t.Fatalf("idempotent code=%d", rr2.Code)
	}

	req3 := httptest.NewRequest(http.MethodGet, "/v1/runs/r1", nil)
	req3.Header.Set("Authorization", "Bearer local-dev")
	rr3 := httptest.NewRecorder()
	h.ServeHTTP(rr3, req3)
	if rr3.Code != 200 {
		t.Fatal(rr3.Body.String())
	}
	var m map[string]any
	_ = json.Unmarshal(rr3.Body.Bytes(), &m)
	if m["id"] != "r1" {
		t.Fatalf("%v", m)
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
