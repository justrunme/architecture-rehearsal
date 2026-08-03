// Package status posts CI commit statuses (GitHub Checks / GitLab).
package status

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

// GitHub posts a commit status (and optional check run summary).
type GitHub struct {
	Token      string
	Owner      string
	Repo       string
	APIBase    string // default https://api.github.com
	HTTPClient *http.Client
}

// Status is a commit status payload.
type Status struct {
	State       string // error, failure, pending, success
	Description string
	Context     string
	TargetURL   string
}

// FromEnvGitHub builds client from GITHUB_TOKEN, GITHUB_REPOSITORY.
func FromEnvGitHub() (*GitHub, error) {
	tok := os.Getenv("GITHUB_TOKEN")
	if tok == "" {
		return nil, fmt.Errorf("GITHUB_TOKEN required")
	}
	repo := os.Getenv("GITHUB_REPOSITORY") // owner/repo
	if repo == "" {
		return nil, fmt.Errorf("GITHUB_REPOSITORY required")
	}
	var owner, name string
	for i := 0; i < len(repo); i++ {
		if repo[i] == '/' {
			owner, name = repo[:i], repo[i+1:]
			break
		}
	}
	if owner == "" || name == "" {
		return nil, fmt.Errorf("invalid GITHUB_REPOSITORY")
	}
	return &GitHub{Token: tok, Owner: owner, Repo: name, APIBase: "https://api.github.com"}, nil
}

// PostCommitStatus posts to /repos/{owner}/{repo}/statuses/{sha}
func (g *GitHub) PostCommitStatus(sha string, st Status) error {
	if st.Context == "" {
		st.Context = "architecture-rehearsal"
	}
	body := map[string]string{
		"state":       st.State,
		"description": truncate(st.Description, 140),
		"context":     st.Context,
	}
	if st.TargetURL != "" {
		body["target_url"] = st.TargetURL
	}
	url := fmt.Sprintf("%s/repos/%s/%s/statuses/%s", g.base(), g.Owner, g.Repo, sha)
	return g.post(url, body)
}

// PostCheckRun creates a completed check run (Checks API).
func (g *GitHub) PostCheckRun(sha, name, conclusion, summary string) error {
	body := map[string]any{
		"name":        name,
		"head_sha":    sha,
		"status":      "completed",
		"conclusion":  conclusion, // success, failure, neutral, cancelled
		"completed_at": time.Now().UTC().Format(time.RFC3339),
		"output": map[string]string{
			"title":   name,
			"summary": summary,
		},
	}
	url := fmt.Sprintf("%s/repos/%s/%s/check-runs", g.base(), g.Owner, g.Repo)
	return g.post(url, body)
}

// MapDecision converts rehearsal decision to GitHub state.
func MapDecision(decision string, exitCode int) (state, conclusion string) {
	switch {
	case exitCode == 0 || decision == "approve":
		return "success", "success"
	case exitCode == 1 || decision == "warn":
		return "success", "neutral"
	case exitCode == 3 || decision == "block":
		return "failure", "failure"
	case exitCode == 4:
		return "error", "neutral"
	default:
		return "error", "failure"
	}
}

func (g *GitHub) base() string {
	if g.APIBase != "" {
		return g.APIBase
	}
	return "https://api.github.com"
}

func (g *GitHub) post(url string, body any) error {
	raw, _ := json.Marshal(body)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+g.Token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")
	cli := g.HTTPClient
	if cli == nil {
		cli = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := cli.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("github api %d: %s", resp.StatusCode, b)
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
