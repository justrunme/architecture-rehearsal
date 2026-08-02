package evidence

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/justrunme/architecture-rehearsal/internal/analyze"
	"github.com/justrunme/architecture-rehearsal/internal/report"
)

// Bundle writes JSON report + HTML + copied inputs under outDir.
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
	if err := os.WriteFile(filepath.Join(dir, "report.json"), raw, 0o644); err != nil {
		return "", err
	}
	if err := report.WriteHTML(filepath.Join(dir, "report.html"), rep); err != nil {
		return "", err
	}
	_ = copyFile(baselinePath, filepath.Join(dir, "baseline.json"))
	_ = copyFile(changePath, filepath.Join(dir, "change.json"))

	man := map[string]any{
		"tool":      "architecture-rehearsal",
		"version":   analyze.Version,
		"changeId":  rep.ChangeID,
		"risk":      rep.Risk,
		"decision":  rep.Decision,
		"generated": rep.Generated,
		"files":     []string{"report.json", "report.html", "baseline.json", "change.json"},
	}
	mb, _ := json.MarshalIndent(man, "", "  ")
	_ = os.WriteFile(filepath.Join(dir, "evidence-manifest.json"), mb, 0o644)
	_ = os.WriteFile(filepath.Join(outDir, "latest-report.json"), raw, 0o644)
	return dir, nil
}

func copyFile(src, dst string) error {
	if src == "" {
		return nil
	}
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0o644)
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
