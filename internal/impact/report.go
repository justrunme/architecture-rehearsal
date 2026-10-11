package impact

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

// WriteJSON writes the impact report as JSON.
func WriteJSON(path string, rep *Report) error {
	raw, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o644)
}

// Markdown renders a PR-oriented markdown report.
func Markdown(rep *Report) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Argo CD change-impact report\n\n")
	if rep.AppName != "" {
		fmt.Fprintf(&b, "- **Application:** `%s`\n", rep.AppName)
	}
	if rep.Revision != "" {
		fmt.Fprintf(&b, "- **Revision:** `%s`\n", rep.Revision)
	}
	if rep.ChangeID != "" {
		fmt.Fprintf(&b, "- **Change ID:** `%s`\n", rep.ChangeID)
	}
	fmt.Fprintf(&b, "- **Gate verdict:** **%s**\n", rep.Verdict)
	fmt.Fprintf(&b, "- **Confidence:** %s\n", rep.Confidence)
	if rep.AnalyzeDecision != "" {
		fmt.Fprintf(&b, "- **Analyze decision:** `%s` (risk=%s)\n", rep.AnalyzeDecision, rep.AnalyzeRisk)
	}
	fmt.Fprintf(&b, "- **Generated:** %s\n\n", time.Now().UTC().Format(time.RFC3339))

	fmt.Fprintf(&b, "## Summary\n\n%s\n\n", rep.Summary)

	fmt.Fprintf(&b, "## Detectors\n\n")
	fmt.Fprintf(&b, "| Detector | Status | Confidence | Explanation |\n")
	fmt.Fprintf(&b, "|----------|--------|------------|-------------|\n")
	for _, d := range rep.Detectors {
		fmt.Fprintf(&b, "| `%s` | **%s** | %s | %s |\n",
			d.Detector, d.Status, d.Confidence, escapePipe(d.Explanation))
	}
	b.WriteByte('\n')

	fmt.Fprintf(&b, "## Affected workloads\n\n")
	if len(rep.Affected) == 0 {
		fmt.Fprintf(&b, "_None beyond change seeds (or not determined)._\n\n")
	} else {
		for _, a := range rep.Affected {
			fmt.Fprintf(&b, "- `%s`\n", a)
		}
		b.WriteByte('\n')
	}

	fmt.Fprintf(&b, "## Predicted failures\n\n")
	if len(rep.Predicted) == 0 {
		fmt.Fprintf(&b, "_None predicted._\n\n")
	} else {
		for _, p := range rep.Predicted {
			fmt.Fprintf(&b, "- %s\n", p)
		}
		b.WriteByte('\n')
	}

	fmt.Fprintf(&b, "## Missing data\n\n")
	if len(rep.Missing) == 0 {
		fmt.Fprintf(&b, "_Required coverage present for evaluated detectors._\n\n")
	} else {
		fmt.Fprintf(&b, "Gate cannot safely PASS while these are missing:\n\n")
		for _, m := range rep.Missing {
			fmt.Fprintf(&b, "- `%s`\n", m)
		}
		b.WriteByte('\n')
	}

	fmt.Fprintf(&b, "---\n_Architecture Rehearsal explains consequences of the desired sync; it does not re-implement Argo CD diff._\n")
	return b.String()
}

// WriteMarkdown writes the markdown report to path.
func WriteMarkdown(path string, rep *Report) error {
	return os.WriteFile(path, []byte(Markdown(rep)), 0o644)
}

// CheckSummary is a short text suitable for GitHub Check Run output.summary.
func CheckSummary(rep *Report) string {
	var b strings.Builder
	fmt.Fprintf(&b, "**Argo impact gate: %s** (confidence=%s)\n\n", rep.Verdict, rep.Confidence)
	fmt.Fprintf(&b, "%s\n\n", rep.Summary)
	for _, d := range rep.Detectors {
		fmt.Fprintf(&b, "- `%s`: **%s** — %s\n", d.Detector, d.Status, d.Explanation)
	}
	if len(rep.Missing) > 0 {
		fmt.Fprintf(&b, "\nMissing data: %s\n", strings.Join(rep.Missing, ", "))
	}
	return b.String()
}

func escapePipe(s string) string {
	return strings.ReplaceAll(s, "|", "\\|")
}
