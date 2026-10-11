package change_test

import (
	"path/filepath"
	"runtime"
	"testing"

	"github.com/justrunme/architecture-rehearsal/internal/analyze"
	"github.com/justrunme/architecture-rehearsal/internal/change"
	"github.com/justrunme/architecture-rehearsal/internal/graph"
	"github.com/justrunme/architecture-rehearsal/internal/loader"
	"github.com/justrunme/architecture-rehearsal/internal/validate"
)

func tfFixture(t *testing.T, name string) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Join(filepath.Dir(file), "testdata", "tfplans", name)
}

func TestTerraformClassifierTable(t *testing.T) {
	base, err := loader.LoadSnapshot("../../examples/golden/rwo-node-loss/baseline.json")
	if err != nil {
		t.Fatal(err)
	}
	cniBase, err := loader.LoadSnapshot("../../examples/golden/cni-ip-capacity/baseline.json")
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name           string
		fixture        string
		base           *graph.Snapshot
		wantKind       string
		wantScenario   string
		wantDesired    any
		wantLostNode   string
		wantUnsupported int
		wantAddrs      []string
		wantGap        string
	}{
		{
			name:         "eks node group scale",
			fixture:      "eks_node_group_scale.json",
			base:         cniBase,
			wantKind:     "terraform-plan",
			wantScenario: "cni-ip-capacity",
			wantDesired:  float64(5),
		},
		{
			name:         "gke node pool",
			fixture:      "gke_node_pool.json",
			base:         cniBase,
			wantKind:     "terraform-plan",
			wantScenario: "cni-ip-capacity",
			wantDesired:  float64(6),
		},
		{
			name:         "aws instance delete resolves node",
			fixture:      "aws_instance_delete.json",
			base:         base,
			wantKind:     "node-failure",
			wantScenario: "rwo-node-loss",
			wantLostNode: "node/ip-10-0-12-44",
		},
		{
			name:            "unsupported s3 and iam",
			fixture:         "unsupported_s3.json",
			base:            base,
			wantKind:        "terraform-plan",
			wantUnsupported: 2,
			wantAddrs:       []string{"aws_s3_bucket.logs", "aws_iam_role.extra"},
			wantGap:         "unsupported_resource_changes",
		},
		{
			name:         "asg plus security group rule",
			fixture:      "mixed_asg_and_sg.json",
			base:         cniBase,
			wantKind:     "terraform-plan",
			wantScenario: "cni-ip-capacity",
			wantDesired:  float64(3),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ch, err := change.FromTerraformPlan(tfFixture(t, tc.fixture), "tf-"+tc.name, tc.name, tc.base)
			if err != nil {
				t.Fatal(err)
			}
			if ch.Kind != tc.wantKind {
				t.Fatalf("kind=%q want %q", ch.Kind, tc.wantKind)
			}
			if tc.wantScenario != "" && ch.Facts["scenario"] != tc.wantScenario {
				t.Fatalf("scenario=%v want %s", ch.Facts["scenario"], tc.wantScenario)
			}
			if tc.wantDesired != nil && ch.Facts["terraform_desired_size"] != tc.wantDesired {
				t.Fatalf("desired_size=%v (%T) want %v", ch.Facts["terraform_desired_size"], ch.Facts["terraform_desired_size"], tc.wantDesired)
			}
			if tc.wantLostNode != "" {
				if ch.Facts["lost_node"] != tc.wantLostNode {
					t.Fatalf("lost_node=%v want %s", ch.Facts["lost_node"], tc.wantLostNode)
				}
				if len(ch.RemovedNodes) != 1 || ch.RemovedNodes[0] != tc.wantLostNode {
					t.Fatalf("RemovedNodes=%v", ch.RemovedNodes)
				}
			}
			if tc.wantUnsupported > 0 {
				n, _ := ch.Facts["unsupported_resource_changes"].(int)
				if n != tc.wantUnsupported {
					t.Fatalf("unsupported=%v want %d facts=%v", ch.Facts["unsupported_resource_changes"], tc.wantUnsupported, ch.Facts)
				}
				addrs, _ := ch.Facts["unsupported_resource_addresses"].([]string)
				if len(addrs) != len(tc.wantAddrs) {
					t.Fatalf("addrs=%v want %v", addrs, tc.wantAddrs)
				}
				for i := range tc.wantAddrs {
					if addrs[i] != tc.wantAddrs[i] {
						t.Fatalf("addrs=%v want %v", addrs, tc.wantAddrs)
					}
				}
			}
			if tc.wantGap != "" && ch.Facts["coverage_gap"] != tc.wantGap {
				t.Fatalf("coverage_gap=%v want %s", ch.Facts["coverage_gap"], tc.wantGap)
			}
			if err := validate.ChangeAgainstBaseline(tc.base, ch); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestUnsupportedResourcesSurfaceInAnalyze(t *testing.T) {
	base, err := loader.LoadSnapshot("../../examples/golden/cni-ip-capacity/baseline.json")
	if err != nil {
		t.Fatal(err)
	}
	ch, err := change.FromTerraformPlan(tfFixture(t, "unsupported_s3.json"), "tf-unsup", "unsupported", base)
	if err != nil {
		t.Fatal(err)
	}
	rep, err := analyze.Run(base, ch)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Decision == analyze.DecisionApprove {
		t.Fatalf("unsupported resources must not approve: %s gaps=%v", rep.Decision, rep.Coverage.Gaps)
	}
	found := false
	for _, g := range rep.Coverage.Gaps {
		if g == "unsupported_resource_changes=2" || len(g) > 0 && (g == "change coverage_gap: unsupported_resource_changes" ||
			contains(g, "aws_s3_bucket.logs")) {
			found = true
		}
	}
	for _, m := range rep.Coverage.RequiredMissing {
		if m == "unsupported_resource_changes" || m == "coverage_gap:unsupported_resource_changes" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected unsupported surfaced in coverage: missing=%v gaps=%v", rep.Coverage.RequiredMissing, rep.Coverage.Gaps)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 || (len(s) > 0 && stringIndex(s, sub) >= 0))
}

func stringIndex(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func TestTerraformSeedsValidFixture(t *testing.T) {
	base, err := loader.LoadSnapshot("../../examples/golden/cni-ip-capacity/baseline.json")
	if err != nil {
		t.Fatal(err)
	}
	ch, err := change.FromTerraformPlan(tfFixture(t, "eks_node_group_scale.json"), "tf1", "scale ng", base)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range ch.Seeds {
		if len(s) >= 3 && s[:3] == "tf:" {
			t.Fatalf("invalid tf: seed %s", s)
		}
	}
	if err := validate.ChangeAgainstBaseline(base, ch); err != nil {
		t.Fatal(err)
	}
}
