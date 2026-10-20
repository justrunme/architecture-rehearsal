package report_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/justrunme/architecture-rehearsal/internal/analyze"
	"github.com/justrunme/architecture-rehearsal/internal/report"
	"github.com/justrunme/architecture-rehearsal/internal/scenario"
)

func TestWriteHTMLReport(t *testing.T) {
	dir := t.TempDir()
	outPath := filepath.Join(dir, "report.html")

	rep := &analyze.Report{
		Version:     "1.7.1",
		ChangeID:    "ch-100",
		ChangeTitle: "Deploy Payments Service",
		Risk:        "high",
		Decision:    "block",
		Summary:     "High risk deployment affecting 3 critical paths",
		Rollback:    "unavailable",
		Generated:   time.Now().UTC(),
		Findings: []scenario.Finding{
			{
				Scenario: "volume-az",
				Title:    "RWO Volume AZ Mismatch",
				Risk:     "high",
				Summary:  "Volume is in us-east-1a, pod scheduled in us-east-1b",
				Cascade:  []string{"Pod Pending", "Service Outage"},
				Controls: []string{"Use TopologySpreadConstraints"},
				Evidence: []string{"pv/pv-1", "pod/pod-1"},
			},
		},
		CoverageGaps: []string{"Missing PromQL rule check"},
	}

	if err := report.WriteHTML(outPath, rep); err != nil {
		t.Fatalf("WriteHTML failed: %v", err)
	}

	content, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("failed to read written HTML report: %v", err)
	}

	if len(content) == 0 {
		t.Fatal("expected non-empty HTML report content")
	}
}
