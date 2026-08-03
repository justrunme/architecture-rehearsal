package status_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/justrunme/architecture-rehearsal/internal/integrations/status"
)

func TestMapDecision(t *testing.T) {
	st, conc := status.MapDecision("block", 3)
	if st != "failure" || conc != "failure" {
		t.Fatalf("%s %s", st, conc)
	}
	st, conc = status.MapDecision("approve", 0)
	if st != "success" {
		t.Fatal(st)
	}
}

func TestGitHubPostCommitStatus(t *testing.T) {
	var got map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			t.Fatalf("auth %s", r.Header.Get("Authorization"))
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(201)
	}))
	defer srv.Close()
	g := &status.GitHub{Token: "tok", Owner: "o", Repo: "r", APIBase: srv.URL, HTTPClient: srv.Client()}
	if err := g.PostCommitStatus("abc", status.Status{State: "success", Description: "ok"}); err != nil {
		t.Fatal(err)
	}
	if got["state"] != "success" || got["context"] != "architecture-rehearsal" {
		t.Fatalf("%v", got)
	}
}

func TestGitLabPostCommitStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("state") != "success" {
			t.Fatalf("%s", r.URL.RawQuery)
		}
		w.WriteHeader(201)
	}))
	defer srv.Close()
	g := &status.GitLab{Token: "t", BaseURL: srv.URL, ProjectID: "1", HTTPClient: srv.Client()}
	if err := g.PostCommitStatus("sha", status.Status{State: "success", Description: "ok"}); err != nil {
		t.Fatal(err)
	}
}
