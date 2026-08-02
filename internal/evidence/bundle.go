package evidence

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/justrunme/architecture-rehearsal/internal/analyze"
	"github.com/justrunme/architecture-rehearsal/internal/graph"
	"github.com/justrunme/architecture-rehearsal/internal/report"
)

// FileEntry is a hashed artifact in the evidence manifest.
type FileEntry struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
	Bytes  int    `json:"bytes"`
}

// Manifest is a fail-closed evidence record.
type Manifest struct {
	APIVersion string      `json:"apiVersion"`
	Kind       string      `json:"kind"`
	Tool       string      `json:"tool"`
	Version    string      `json:"version"`
	ChangeID   string      `json:"changeId"`
	Risk       string      `json:"risk"`
	Decision   string      `json:"decision"`
	Digest     string      `json:"semanticDigest,omitempty"`
	Generated  time.Time   `json:"generatedAt"`
	Files      []FileEntry `json:"files"`
}

// Bundle writes JSON report + HTML + inputs with SHA-256. Fail-closed.
func Bundle(outDir string, rep *analyze.Report, baselinePath, changePath string) (string, error) {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return "", err
	}
	stamp := time.Now().UTC().Format("20060102T150405Z")
	dir := filepath.Join(outDir, fmt.Sprintf("%s-%s", sanitize(rep.ChangeID), stamp))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}

	raw, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return "", err
	}
	files := []struct {
		name string
		data []byte
	}{}
	files = append(files, struct {
		name string
		data []byte
	}{"report.json", raw})

	htmlPath := filepath.Join(dir, "report.html")
	if err := report.WriteHTML(htmlPath, rep); err != nil {
		return "", fmt.Errorf("write report.html: %w", err)
	}
	htmlBytes, err := os.ReadFile(htmlPath)
	if err != nil {
		return "", err
	}
	files = append(files, struct {
		name string
		data []byte
	}{"report.html", htmlBytes})

	baseBytes, err := os.ReadFile(baselinePath)
	if err != nil {
		return "", fmt.Errorf("read baseline for evidence: %w", err)
	}
	files = append(files, struct {
		name string
		data []byte
	}{"baseline.json", baseBytes})

	chBytes, err := os.ReadFile(changePath)
	if err != nil {
		return "", fmt.Errorf("read change for evidence: %w", err)
	}
	files = append(files, struct {
		name string
		data []byte
	}{"change.json", chBytes})

	var entries []FileEntry
	for _, f := range files {
		p := filepath.Join(dir, f.name)
		if f.name != "report.html" { // already written
			if err := os.WriteFile(p, f.data, 0o644); err != nil {
				return "", fmt.Errorf("write %s: %w", f.name, err)
			}
		}
		sum := sha256.Sum256(f.data)
		entries = append(entries, FileEntry{
			Name:   f.name,
			SHA256: hex.EncodeToString(sum[:]),
			Bytes:  len(f.data),
		})
	}

	man := Manifest{
		APIVersion: graph.APIVersionV1Alpha1,
		Kind:       graph.DocKindEvidence,
		Tool:       "architecture-rehearsal",
		Version:    analyze.Version,
		ChangeID:   rep.ChangeID,
		Risk:       rep.Risk,
		Decision:   rep.Decision,
		Digest:     rep.SemanticDigest,
		Generated:  rep.Generated,
		Files:      entries,
	}
	mb, err := json.MarshalIndent(man, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, "evidence-manifest.json"), mb, 0o644); err != nil {
		return "", fmt.Errorf("write evidence-manifest: %w", err)
	}
	if err := os.WriteFile(filepath.Join(outDir, "latest-report.json"), raw, 0o644); err != nil {
		return "", err
	}
	return dir, nil
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
		return "change"
	}
	return string(out)
}
