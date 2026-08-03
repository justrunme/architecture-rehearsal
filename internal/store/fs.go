// Package store persists rehearsal runs for audit and replay (v0.7 platform layer).
package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/justrunme/architecture-rehearsal/internal/analyze"
	"github.com/justrunme/architecture-rehearsal/internal/graph"
)

// RunRecord is one persisted analyze/verify cycle.
type RunRecord struct {
	APIVersion string    `json:"apiVersion"`
	Kind       string    `json:"kind"`
	ID         string    `json:"id"`
	CreatedAt  time.Time `json:"createdAt"`
	Actor      string    `json:"actor,omitempty"`
	ChangeID   string    `json:"changeId"`
	BaselineID string    `json:"baselineId"`
	Decision   string    `json:"decision"`
	Risk       string    `json:"risk"`
	Digest     string    `json:"semanticDigest,omitempty"`
	EvidenceDir string   `json:"evidenceDir,omitempty"`
	ReportPath  string   `json:"reportPath,omitempty"`
	VerifyOutcome string `json:"verifyOutcome,omitempty"`
	Labels     map[string]string `json:"labels,omitempty"`
}

// AuditEvent is an append-only audit trail entry.
type AuditEvent struct {
	Time   time.Time `json:"time"`
	Actor  string    `json:"actor,omitempty"`
	Action string    `json:"action"`
	RunID  string    `json:"runId,omitempty"`
	Detail string    `json:"detail,omitempty"`
}

// FS is a filesystem-backed run store.
type FS struct {
	Root string
}

// NewFS creates (or opens) a store under root.
func NewFS(root string) (*FS, error) {
	if root == "" {
		root = "out/runs"
	}
	for _, sub := range []string{"", "runs", "audit"} {
		p := root
		if sub != "" {
			p = filepath.Join(root, sub)
		}
		if err := os.MkdirAll(p, 0o755); err != nil {
			return nil, err
		}
	}
	return &FS{Root: root}, nil
}

// SaveRun writes a run record and appends an audit event.
func (s *FS) SaveRun(rec RunRecord, actor string) (string, error) {
	if rec.ID == "" {
		rec.ID = fmt.Sprintf("run-%s", time.Now().UTC().Format("20060102T150405Z"))
	}
	if rec.APIVersion == "" {
		rec.APIVersion = graph.APIVersionV1Alpha1
	}
	if rec.Kind == "" {
		rec.Kind = "RehearsalRun"
	}
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = time.Now().UTC()
	}
	if rec.Actor == "" {
		rec.Actor = actor
	}
	path := filepath.Join(s.Root, "runs", sanitize(rec.ID)+".json")
	raw, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		return "", err
	}
	_ = s.AppendAudit(AuditEvent{
		Time:   time.Now().UTC(),
		Actor:  rec.Actor,
		Action: "run.save",
		RunID:  rec.ID,
		Detail: fmt.Sprintf("decision=%s risk=%s", rec.Decision, rec.Risk),
	})
	return path, nil
}

// SaveFromReport creates a run from an analyze report.
func (s *FS) SaveFromReport(rep *analyze.Report, evidenceDir, actor string) (string, error) {
	return s.SaveRun(RunRecord{
		ChangeID:    rep.ChangeID,
		BaselineID:  rep.BaselineID,
		Decision:    rep.Decision,
		Risk:        rep.Risk,
		Digest:      rep.SemanticDigest,
		EvidenceDir: evidenceDir,
		Labels: map[string]string{
			"tool":    "architecture-rehearsal",
			"version": rep.Version,
		},
	}, actor)
}

// ListRuns returns run IDs newest-first.
func (s *FS) ListRuns() ([]RunRecord, error) {
	dir := filepath.Join(s.Root, "runs")
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []RunRecord
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var rec RunRecord
		if json.Unmarshal(raw, &rec) == nil {
			out = append(out, rec)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out, nil
}

// AppendAudit appends one line to the audit log (JSONL).
func (s *FS) AppendAudit(ev AuditEvent) error {
	if ev.Time.IsZero() {
		ev.Time = time.Now().UTC()
	}
	path := filepath.Join(s.Root, "audit", "audit.jsonl")
	raw, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(raw, '\n'))
	return err
}

// ReadAudit returns recent audit events (max n, 0 = all).
func (s *FS) ReadAudit(max int) ([]AuditEvent, error) {
	path := filepath.Join(s.Root, "audit", "audit.jsonl")
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []AuditEvent
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var ev AuditEvent
		if json.Unmarshal([]byte(line), &ev) == nil {
			out = append(out, ev)
		}
	}
	if max > 0 && len(out) > max {
		out = out[len(out)-max:]
	}
	return out, nil
}

func sanitize(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			out = append(out, r)
		} else {
			out = append(out, '-')
		}
	}
	if len(out) == 0 {
		return "run"
	}
	return string(out)
}
