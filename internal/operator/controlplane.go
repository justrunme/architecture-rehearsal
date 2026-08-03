// Package operator reconciles RehearsalRun CRD-shaped JSON and optional control-plane API.
package operator

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/justrunme/architecture-rehearsal/internal/run"
)

// ControlPlaneClient talks to rehearsal serve HTTP API.
// BaseURL must come only from operator deployment config — never from user CR fields.
type ControlPlaneClient struct {
	BaseURL string
	Token   string
	Org     string
	HTTP    *http.Client
}

func (c *ControlPlaneClient) client() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 60 * time.Second}
}

func (c *ControlPlaneClient) do(method, path string, body any) (*http.Response, error) {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, strings.TrimRight(c.BaseURL, "/")+path, rdr)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json")
	return c.client().Do(req)
}

// EnsureResult is the outcome of EnsureRun.
type EnsureResult struct {
	Created  bool
	Conflict bool // 409: run id already exists with different identity
	Run      *run.RehearsalRun
}

// EnsureRun creates a run. On 409, fetches existing and returns Conflict=true
// so the caller can detect spec drift (does NOT treat 409 as silent success).
func (c *ControlPlaneClient) EnsureRun(rr *run.RehearsalRun) (*EnsureResult, error) {
	body := map[string]any{
		"id":             rr.ID,
		"idempotencyKey": rr.IdempotencyKey,
		"baselineRef":    rr.Spec.BaselineRef,
		"changeRef":      rr.Spec.ChangeRef,
		"observedRef":    rr.Spec.ObservedRef,
		"clusterName":    rr.Spec.ClusterName,
		"scenarios":      rr.Spec.Scenarios,
	}
	if rr.Labels != nil {
		body["project"] = rr.Labels["project"]
		body["environment"] = rr.Labels["environment"]
	}
	resp, err := c.do(http.MethodPost, "/v1/runs", body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	switch resp.StatusCode {
	case 201:
		var created run.RehearsalRun
		_ = json.Unmarshal(raw, &created)
		return &EnsureResult{Created: true, Run: &created}, nil
	case 200:
		// idempotent create (same idempotency key)
		var existing run.RehearsalRun
		_ = json.Unmarshal(raw, &existing)
		return &EnsureResult{Created: false, Run: &existing}, nil
	case 409:
		// Conflict: id exists — fetch and let caller compare digests
		existing, gerr := c.GetRun(rr.ID)
		if gerr != nil {
			return &EnsureResult{Conflict: true}, fmt.Errorf("create run conflict (409) and get failed: %w", gerr)
		}
		return &EnsureResult{Conflict: true, Run: existing}, nil
	default:
		return nil, fmt.Errorf("create run %d: %s", resp.StatusCode, raw)
	}
}

// AdvanceResult holds advance API response.
type AdvanceResult struct {
	StatusCode int
	JobID      string
	Run        *run.RehearsalRun
	Raw        map[string]any
}

// Advance enqueues or sync-advances a run. Returns jobId when async (202).
func (c *ControlPlaneClient) Advance(runID string, async bool) (*AdvanceResult, error) {
	resp, err := c.do(http.MethodPost, "/v1/runs/"+runID+"/advance", map[string]any{"async": async})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	out := &AdvanceResult{StatusCode: resp.StatusCode}
	_ = json.Unmarshal(raw, &out.Raw)
	if resp.StatusCode != 200 && resp.StatusCode != 202 {
		return out, fmt.Errorf("advance %d: %s", resp.StatusCode, raw)
	}
	if resp.StatusCode == 202 {
		if j, ok := out.Raw["jobId"].(string); ok {
			out.JobID = j
		}
	} else {
		var rr run.RehearsalRun
		if json.Unmarshal(raw, &rr) == nil && rr.ID != "" {
			out.Run = &rr
		}
	}
	return out, nil
}

// GetRun fetches run status.
func (c *ControlPlaneClient) GetRun(runID string) (*run.RehearsalRun, error) {
	resp, err := c.do(http.MethodGet, "/v1/runs/"+runID, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("get run %d", resp.StatusCode)
	}
	var rr run.RehearsalRun
	if err := json.NewDecoder(resp.Body).Decode(&rr); err != nil {
		return nil, err
	}
	return &rr, nil
}

// Reconciler drives RehearsalRun objects from a watch directory.
// If ControlPlane is set, syncs to HTTP API instead of local engine only.
type Reconciler struct {
	WatchDir     string
	WorkDir      string
	Holder       string
	ControlPlane *ControlPlaneClient
	// AsyncAdvance when using control plane.
	AsyncAdvance bool
}

// ReconcileOnce loads all *.json runs and advances non-terminal ones.
func (r *Reconciler) ReconcileOnce() (int, error) {
	if r.WatchDir == "" {
		return 0, fmt.Errorf("WatchDir required")
	}
	ents, err := os.ReadDir(r.WatchDir)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, e := range ents {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		path := filepath.Join(r.WatchDir, e.Name())
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var rr run.RehearsalRun
		if err := json.Unmarshal(raw, &rr); err != nil {
			continue
		}
		if rr.Status.Phase.Terminal() {
			continue
		}
		if r.ControlPlane != nil {
			if _, err := r.ControlPlane.EnsureRun(&rr); err != nil {
				continue
			}
			_, _ = r.ControlPlane.Advance(rr.ID, r.AsyncAdvance)
			if latest, err := r.ControlPlane.GetRun(rr.ID); err == nil && latest != nil {
				rr = *latest
			}
		} else {
			eng := &run.Engine{WorkDir: r.WorkDir, Holder: r.Holder}
			if eng.Holder == "" {
				eng.Holder = "operator"
			}
			_ = eng.Execute(&rr)
		}
		out, _ := json.MarshalIndent(rr, "", "  ")
		_ = os.WriteFile(path, out, 0o644)
		n++
	}
	return n, nil
}

// RunLoop reconciles until stop channel closed.
func (r *Reconciler) RunLoop(stop <-chan struct{}, interval time.Duration) {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			_, _ = r.ReconcileOnce()
		}
	}
}
