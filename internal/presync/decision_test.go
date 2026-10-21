package presync

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/justrunme/architecture-rehearsal/internal/impact"
)

func TestHookExitCode(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{Allow, 0},
		{Warn, 0},
		{Block, 3},
		{Unknown, 4},
		{"allow", 0},
		{"warn", 0},
		{"block", 3},
		{"unknown", 4},
		{"PASS", 5}, // not a PreSync decision token
		{"", 5},
		{"GARBAGE", 5},
	}
	for _, tc := range cases {
		if got := HookExitCode(tc.in); got != tc.want {
			t.Fatalf("HookExitCode(%q)=%d want %d", tc.in, got, tc.want)
		}
	}
}

func TestMapImpactVerdict(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{impact.Pass, Allow},
		{"approve", Allow},
		{impact.Warn, Warn},
		{impact.Block, Block},
		{impact.Unknown, Unknown},
		{"", Unknown},
		{"nope", Unknown},
	}
	for _, tc := range cases {
		if got := MapImpactVerdict(tc.in); got != tc.want {
			t.Fatalf("MapImpactVerdict(%q)=%q want %q", tc.in, got, tc.want)
		}
	}
}

func TestParseOverride(t *testing.T) {
	t.Run("absent", func(t *testing.T) {
		ov, err := ParseOverride(nil)
		if err != nil || ov != nil {
			t.Fatalf("got %#v err=%v", ov, err)
		}
		ov, err = ParseOverride(map[string]string{})
		if err != nil || ov != nil {
			t.Fatalf("got %#v err=%v", ov, err)
		}
	})
	t.Run("false values", func(t *testing.T) {
		for _, v := range []string{"false", "0", "no", "off"} {
			ov, err := ParseOverride(map[string]string{AnnotationBreakGlass: v})
			if err != nil || ov != nil {
				t.Fatalf("%q: %#v err=%v", v, ov, err)
			}
		}
	})
	t.Run("enabled with reason", func(t *testing.T) {
		ov, err := ParseOverride(map[string]string{
			AnnotationBreakGlass:       "true",
			AnnotationBreakGlassReason: "INC-42 emergency rollback",
		})
		if err != nil {
			t.Fatal(err)
		}
		if ov == nil || !ov.Enabled || ov.Reason != "INC-42 emergency rollback" {
			t.Fatalf("got %#v", ov)
		}
	})
	t.Run("enabled without reason fail closed", func(t *testing.T) {
		ov, err := ParseOverride(map[string]string{AnnotationBreakGlass: "1"})
		if err == nil || ov != nil {
			t.Fatalf("expected error, got %#v err=%v", ov, err)
		}
		if !strings.Contains(err.Error(), AnnotationBreakGlassReason) {
			t.Fatalf("error should mention reason annotation: %v", err)
		}
	})
	t.Run("enabled with blank reason fail closed", func(t *testing.T) {
		_, err := ParseOverride(map[string]string{
			AnnotationBreakGlass:       "yes",
			AnnotationBreakGlassReason: "   ",
		})
		if err == nil {
			t.Fatal("expected error")
		}
	})
	t.Run("invalid value fail closed", func(t *testing.T) {
		_, err := ParseOverride(map[string]string{AnnotationBreakGlass: "maybe"})
		if err == nil {
			t.Fatal("expected error")
		}
	})
}

func TestFromImpactAndApplyOverride(t *testing.T) {
	rep := &impact.Report{
		AppName:  "demo",
		ChangeID: "argo-demo",
		Verdict:  impact.Block,
		Summary:  "gate=BLOCK detectors: PASS=4 WARN=0 BLOCK=1 UNKNOWN=0",
		Detectors: []impact.Verdict{
			{Detector: "scheduling-capacity", Status: impact.Block, Explanation: "unschedulable"},
			{Detector: "blast-radius", Status: impact.Pass, Explanation: "ok"},
		},
		Predicted: []string{"[BLOCK] unschedulable"},
	}
	r := FromImpact(rep, 42*time.Millisecond)
	if r.Decision != Block || r.DurationMs != 42 {
		t.Fatalf("got %#v", r)
	}
	if len(r.RuleIDs) != 1 || r.RuleIDs[0] != "scheduling-capacity" {
		t.Fatalf("ruleIds=%v", r.RuleIDs)
	}
	if HookExitCode(r.Decision) != 3 {
		t.Fatalf("exit=%d", HookExitCode(r.Decision))
	}

	ov := &Override{Enabled: true, Reason: "SEV-1"}
	r2 := ApplyOverride(r, ov)
	if r2.Decision != Allow || r2.Override == nil {
		t.Fatalf("override apply: %#v", r2)
	}
	if HookExitCode(r2.Decision) != 0 {
		t.Fatalf("override exit=%d", HookExitCode(r2.Decision))
	}
	if !strings.Contains(r2.Reasons[0], "break-glass") {
		t.Fatalf("reasons=%v", r2.Reasons)
	}
}

func TestFromImpactNilFailClosed(t *testing.T) {
	r := FromImpact(nil, time.Second)
	if r.Decision != Unknown || HookExitCode(r.Decision) == 0 {
		t.Fatalf("nil report must be UNKNOWN non-zero: %#v", r)
	}
}

func TestFromImpact_PassNoteWhenNodesUnreadable(t *testing.T) {
	rep := &impact.Report{
		Verdict: impact.Pass,
		Summary: "gate=PASS detectors: PASS=1 WARN=0 BLOCK=0 UNKNOWN=0",
		Detectors: []impact.Verdict{{
			Detector:    "scheduling-capacity",
			Status:      impact.Pass,
			Explanation: "no Node objects in baseline; change does not need capacity or touch scheduling-relevant fields",
			Evidence:    []string{"nodes collect failed: forbidden"},
		}},
	}
	r := FromImpact(rep, time.Millisecond)
	if r.Decision != Allow {
		t.Fatalf("decision=%s", r.Decision)
	}
	joined := strings.Join(r.Reasons, " | ")
	if !strings.Contains(joined, "no Node objects") {
		t.Fatalf("PASS note about missing nodes must appear in reasons, got %v", r.Reasons)
	}
	if !strings.Contains(joined, "nodes collect") {
		t.Fatalf("nodes collect evidence must appear in reasons, got %v", r.Reasons)
	}
}

func TestWriteJSONLine(t *testing.T) {
	var buf bytes.Buffer
	r := Result{Decision: Allow, Reasons: []string{"ok"}, RuleIDs: []string{}, DurationMs: 7}
	if err := WriteJSONLine(&buf, r); err != nil {
		t.Fatal(err)
	}
	line := strings.TrimSpace(buf.String())
	if strings.Count(line, "\n") != 0 {
		t.Fatalf("expected one line, got %q", buf.String())
	}
	var got Result
	if err := json.Unmarshal([]byte(line), &got); err != nil {
		t.Fatal(err)
	}
	if got.Decision != Allow || got.DurationMs != 7 {
		t.Fatalf("got %#v", got)
	}
}
