package status

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"time"
)

// GitLab posts commit statuses via Commit Status API.
type GitLab struct {
	Token      string
	BaseURL    string // e.g. https://gitlab.com/api/v4
	ProjectID  string // numeric or URL-encoded path
	HTTPClient *http.Client
}

// FromEnvGitLab builds from CI_JOB_TOKEN / GITLAB_TOKEN and CI_PROJECT_ID.
func FromEnvGitLab() (*GitLab, error) {
	tok := os.Getenv("GITLAB_TOKEN")
	if tok == "" {
		tok = os.Getenv("CI_JOB_TOKEN")
	}
	if tok == "" {
		return nil, fmt.Errorf("GITLAB_TOKEN or CI_JOB_TOKEN required")
	}
	pid := os.Getenv("CI_PROJECT_ID")
	if pid == "" {
		return nil, fmt.Errorf("CI_PROJECT_ID required")
	}
	base := os.Getenv("CI_API_V4_URL")
	if base == "" {
		base = "https://gitlab.com/api/v4"
	}
	return &GitLab{Token: tok, BaseURL: base, ProjectID: pid}, nil
}

// PostCommitStatus posts to projects/:id/statuses/:sha
func (g *GitLab) PostCommitStatus(sha string, st Status) error {
	if st.Context == "" {
		st.Context = "architecture-rehearsal"
	}
	// GitLab state: pending, running, success, failed, canceled
	state := st.State
	switch state {
	case "failure", "error":
		state = "failed"
	case "success":
		state = "success"
	case "pending":
		state = "pending"
	}
	q := url.Values{}
	q.Set("state", state)
	q.Set("name", st.Context)
	q.Set("description", truncate(st.Description, 255))
	if st.TargetURL != "" {
		q.Set("target_url", st.TargetURL)
	}
	u := fmt.Sprintf("%s/projects/%s/statuses/%s?%s",
		trimSlash(g.BaseURL), url.PathEscape(g.ProjectID), sha, q.Encode())
	req, err := http.NewRequest(http.MethodPost, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("PRIVATE-TOKEN", g.Token)
	// job token alternative
	if os.Getenv("CI_JOB_TOKEN") == g.Token {
		req.Header.Set("JOB-TOKEN", g.Token)
		req.Header.Del("PRIVATE-TOKEN")
	}
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
		return fmt.Errorf("gitlab api %d: %s", resp.StatusCode, b)
	}
	return nil
}

// PostJSON is available for extensions.
func (g *GitLab) PostJSON(path string, body any) error {
	raw, _ := json.Marshal(body)
	req, err := http.NewRequest(http.MethodPost, trimSlash(g.BaseURL)+path, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("PRIVATE-TOKEN", g.Token)
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
		return fmt.Errorf("gitlab api %d: %s", resp.StatusCode, b)
	}
	return nil
}

func trimSlash(s string) string {
	for len(s) > 0 && s[len(s)-1] == '/' {
		s = s[:len(s)-1]
	}
	return s
}
