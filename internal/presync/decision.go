// Package presync maps Argo CD PreSync hook decisions for the change-impact gate.
//
// Hook exit contract (differs from CLI argo-impact WARN→1):
//
//	ALLOW / WARN → 0 (sync proceeds)
//	BLOCK / UNKNOWN → non-zero (Argo aborts sync)
//
// Fail closed: missing inputs, API errors, timeouts, and unknown decisions exit non-zero.
package presync

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/justrunme/architecture-rehearsal/internal/impact"
)

// Decision vocabulary for PreSync JSON lines (ALLOW instead of PASS).
const (
	Allow   = "ALLOW"
	Warn    = "WARN"
	Block   = "BLOCK"
	Unknown = "UNKNOWN"
)

// Annotation keys on the Argo CD Application for audited break-glass.
const (
	AnnotationBreakGlass       = "rehearsal.io/break-glass"
	AnnotationBreakGlassReason = "rehearsal.io/break-glass-reason"
)

// Override is an explicit, audited break-glass decision.
type Override struct {
	Enabled bool   `json:"enabled"`
	Reason  string `json:"reason"`
}

// Result is one JSON line printed by the PreSync Job.
type Result struct {
	Decision   string    `json:"decision"`
	Reasons    []string  `json:"reasons"`
	RuleIDs    []string  `json:"ruleIds"`
	DurationMs int64     `json:"durationMs"`
	AppName    string    `json:"appName,omitempty"`
	ChangeID   string    `json:"changeId,omitempty"`
	Override   *Override `json:"override,omitempty"`
}

// HookExitCode maps PreSync decisions to Job exit codes.
// ALLOW and WARN → 0; BLOCK → 3; UNKNOWN → 4; anything else → 5 (fail closed).
func HookExitCode(decision string) int {
	switch strings.ToUpper(strings.TrimSpace(decision)) {
	case Allow, Warn:
		return 0
	case Block:
		return 3
	case Unknown:
		return 4
	default:
		return 5
	}
}

// MapImpactVerdict converts impact gate PASS/WARN/BLOCK/UNKNOWN to PreSync vocabulary.
func MapImpactVerdict(v string) string {
	switch strings.ToUpper(strings.TrimSpace(v)) {
	case "PASS", "APPROVE", "ALLOW":
		return Allow
	case "WARN":
		return Warn
	case "BLOCK":
		return Block
	case "UNKNOWN":
		return Unknown
	default:
		return Unknown
	}
}

// ParseOverride reads break-glass annotations. Fail closed when enabled without a reason.
// Returns (nil, nil) when break-glass is not requested.
func ParseOverride(annotations map[string]string) (*Override, error) {
	if annotations == nil {
		return nil, nil
	}
	raw := strings.TrimSpace(annotations[AnnotationBreakGlass])
	if raw == "" {
		return nil, nil
	}
	enabled := false
	switch strings.ToLower(raw) {
	case "true", "1", "yes", "on":
		enabled = true
	case "false", "0", "no", "off":
		return nil, nil
	default:
		return nil, fmt.Errorf("presync: invalid %s value %q (want true/false)", AnnotationBreakGlass, raw)
	}
	if !enabled {
		return nil, nil
	}
	reason := strings.TrimSpace(annotations[AnnotationBreakGlassReason])
	if reason == "" {
		return nil, fmt.Errorf("presync: %s requires non-empty %s (fail closed)", AnnotationBreakGlass, AnnotationBreakGlassReason)
	}
	return &Override{Enabled: true, Reason: reason}, nil
}

// FromImpact builds a PreSync Result from an impact report and wall duration.
func FromImpact(rep *impact.Report, d time.Duration) Result {
	out := Result{
		Decision:   Unknown,
		Reasons:    []string{},
		RuleIDs:    []string{},
		DurationMs: d.Milliseconds(),
	}
	if rep == nil {
		out.Reasons = []string{"impact report missing"}
		return out
	}
	out.Decision = MapImpactVerdict(rep.Verdict)
	out.AppName = rep.AppName
	out.ChangeID = rep.ChangeID
	if rep.Summary != "" {
		out.Reasons = append(out.Reasons, rep.Summary)
	}
	for _, v := range rep.Detectors {
		if v.Status == impact.Pass {
			// Surface PASS-with-note (e.g. no Nodes → capacity skipped) in Job JSON/logs.
			if note := passInformationalNote(v); note != "" {
				out.Reasons = append(out.Reasons, note)
			}
			continue
		}
		out.RuleIDs = append(out.RuleIDs, v.Detector)
		if v.Explanation != "" {
			out.Reasons = append(out.Reasons, fmt.Sprintf("%s: %s", v.Detector, v.Explanation))
		}
	}
	for _, p := range rep.Predicted {
		if p != "" {
			out.Reasons = append(out.Reasons, p)
		}
	}
	if len(out.Reasons) == 0 && out.Decision == Allow {
		out.Reasons = []string{"no blocking impact findings"}
	}
	return out
}

// ApplyOverride forces ALLOW when a valid break-glass override is present and records it.
func ApplyOverride(r Result, ov *Override) Result {
	if ov == nil || !ov.Enabled {
		return r
	}
	r.Override = ov
	r.Decision = Allow
	r.Reasons = append([]string{
		fmt.Sprintf("break-glass override: %s", ov.Reason),
	}, r.Reasons...)
	return r
}

// passInformationalNote returns a Job-visible note for PASS detectors that carry
// an informational explanation (nodes unreadable / capacity not evaluated).
func passInformationalNote(v impact.Verdict) string {
	if v.Explanation == "" {
		return ""
	}
	if v.Detector != "scheduling-capacity" {
		return ""
	}
	if !strings.Contains(v.Explanation, "no Node objects") &&
		!strings.Contains(v.Explanation, "does not need capacity") {
		return ""
	}
	note := fmt.Sprintf("%s: %s", v.Detector, v.Explanation)
	for _, e := range v.Evidence {
		if strings.Contains(e, "nodes collect") || strings.Contains(e, "no Node") {
			note += "; " + e
			break
		}
	}
	return note
}

// WriteJSONLine emits a single JSON decision line (no pretty-print).
func WriteJSONLine(w interface{ Write([]byte) (int, error) }, r Result) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return enc.Encode(r)
}
