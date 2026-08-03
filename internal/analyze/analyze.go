package analyze

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/justrunme/architecture-rehearsal/internal/graph"
	"github.com/justrunme/architecture-rehearsal/internal/loader"
	"github.com/justrunme/architecture-rehearsal/internal/scenario"
	"github.com/justrunme/architecture-rehearsal/internal/validate"
)

const Version = "1.2.1"

// Run builds proposed graph, validates, runs scenarios, returns Report.
func Run(base *graph.Snapshot, ch *loader.ChangeEnvelope) (*Report, error) {
	if err := validate.Snapshot(base); err != nil {
		return nil, err
	}
	if err := validate.ChangeAgainstBaseline(base, ch); err != nil {
		return nil, err
	}

	// Content digests before mutation/apply (v1.1 binding).
	baseDig, err := digestAny(base)
	if err != nil {
		return nil, err
	}
	changeDig, err := digestAny(ch)
	if err != nil {
		return nil, err
	}

	// Capture baseline hash before apply to prove immutability in tests.
	proposed := loader.ApplyChange(base, ch)
	propDig, err := digestAny(proposed)
	if err != nil {
		return nil, err
	}
	baseIdx := graph.BuildIndex(base)
	propIdx := graph.BuildIndex(proposed)

	ctx := scenario.Context{
		Baseline: base,
		Proposed: proposed,
		Change:   ch,
		BaseIdx:  baseIdx,
		PropIdx:  propIdx,
	}
	findings, unknownReqs := scenario.RunAll(ctx, scenario.DefaultRunners())
	sort.SliceStable(findings, func(i, j int) bool {
		if findings[i].Scenario != findings[j].Scenario {
			return findings[i].Scenario < findings[j].Scenario
		}
		return findings[i].ID < findings[j].ID
	})

	cov := computeCoverage(base, ch, findings)
	for _, r := range unknownReqs {
		cov.RequiredMissing = append(cov.RequiredMissing, r.ID)
		cov.Gaps = append(cov.Gaps, r.Message)
	}
	// Active ingestion/coverage gaps must never silently approve (v0.4.1 fail-closed).
	applyActiveCoverageGaps(ch, base, &cov)
	cov.RequiredMissing = uniqueStrings(cov.RequiredMissing)
	for _, f := range findings {
		if f.Risk == RiskUnknown {
			cov.RequiredMissing = append(cov.RequiredMissing, "scenario:"+f.Scenario)
		}
	}
	cov.RequiredMissing = uniqueStrings(cov.RequiredMissing)

	hasConfidentFinding := false
	for _, f := range findings {
		if f.Risk != RiskUnknown && f.Risk != RiskNone && f.Confidence != "low" {
			hasConfidentFinding = true
		}
	}
	// Insufficient forces unknown when we lack a confident matched finding.
	// Active coverage_gap always blocks approve even when empty findings.
	insufficient := len(cov.RequiredMissing) > 0 && !hasConfidentFinding

	rep := &Report{
		APIVersion:        graph.APIVersionV1Alpha1,
		Kind:              graph.DocKindReport,
		Version:           Version,
		Generated:         time.Now().UTC(),
		ChangeID:          ch.ID,
		ChangeTitle:       ch.Title,
		ChangeKind:        ch.EffectiveKind(),
		BaselineID:        base.ID,
		BaselineDigest:    baseDig,
		ChangeDigest:      changeDig,
		ProposedDigest:    propDig,
		Findings:          findings,
		Coverage:          cov,
		CoverageGaps:      cov.Gaps,
		Rollback:          RollbackUnknown,
		PredictedFailures: []string{},
	}

	risk := RiskNone
	sloViolations := 0
	criticalPaths := 0
	affected := map[string]bool{}
	var cascades [][]string
	rollback := RollbackUnknown

	for _, f := range findings {
		risk = MaxRisk(risk, f.Risk)
		switch f.Rollback {
		case scenario.RollbackUnavailable:
			rollback = RollbackUnavailable
		case scenario.RollbackAvailable:
			if rollback != RollbackUnavailable {
				rollback = RollbackAvailable
			}
		}
		if f.SLOImpact != "" {
			sloViolations++
		}
		if f.Risk == RiskCritical || f.Risk == RiskHigh {
			criticalPaths++
		}
		for _, c := range f.Components {
			affected[c] = true
		}
		if len(f.Cascade) > 0 {
			cascades = append(cascades, f.Cascade)
		}
		rep.PredictedFailures = append(rep.PredictedFailures, f.Scenario)
	}
	if len(findings) == 0 {
		rollback = RollbackUnknown
	}
	rep.Risk = risk
	if insufficient {
		rep.Risk = RiskUnknown
	}
	rep.Decision = DecisionFromFindings(risk, findings, cov, insufficient)
	rep.AffectedComponents = len(affected)
	rep.CriticalPaths = criticalPaths
	rep.SLOViolations = sloViolations
	rep.Cascades = cascades
	rep.Rollback = rollback
	rep.PredictedFailures = uniqueStrings(rep.PredictedFailures)
	rep.Summary = summarize(rep, ch)
	rep.SemanticDigest = semanticDigest(rep)
	return rep, nil
}

func summarize(r *Report, ch *loader.ChangeEnvelope) string {
	if r.Decision == DecisionUnknown {
		return fmt.Sprintf("Insufficient evidence for a safe gate decision on %q. Missing: %s",
			ch.Title, strings.Join(r.Coverage.RequiredMissing, ", "))
	}
	if len(r.Findings) == 0 {
		return fmt.Sprintf("No deterministic risk patterns matched for change %q. Review coverage gaps before treating this as safe.", ch.Title)
	}
	var parts []string
	parts = append(parts, fmt.Sprintf("%d finding(s), risk=%s, decision=%s", len(r.Findings), r.Risk, r.Decision))
	for _, f := range r.Findings {
		parts = append(parts, f.Title)
	}
	return strings.Join(parts, " · ")
}

func computeCoverage(base *graph.Snapshot, ch *loader.ChangeEnvelope, findings []scenario.Finding) Coverage {
	cov := Coverage{
		Domains: map[string]float64{
			"kubernetes":    0,
			"network":       0,
			"iam":           0,
			"observability": 0,
		},
		Gaps: []string{},
	}
	kinds := map[graph.Kind]bool{}
	for _, n := range base.Nodes {
		kinds[n.Kind] = true
	}
	// kubernetes domain
	kScore := 0.0
	if kinds[graph.KindNode] {
		kScore += 0.35
	} else {
		cov.Gaps = append(cov.Gaps, "no Node objects — topology/capacity limited")
	}
	if kinds[graph.KindWorkload] {
		kScore += 0.35
	} else {
		cov.Gaps = append(cov.Gaps, "no Workload objects")
	}
	if kinds[graph.KindPVC] {
		kScore += 0.15
	}
	if kinds[graph.KindService] {
		kScore += 0.15
	}
	cov.Domains["kubernetes"] = kScore

	// observability
	oScore := 0.0
	if base.Meta != nil {
		if _, ok := base.Meta["metric_labels"]; ok {
			oScore += 0.7
		} else {
			cov.Gaps = append(cov.Gaps, "no meta.metric_labels — prom scenarios limited")
		}
		if _, ok := base.Meta["metrics"]; ok {
			oScore += 0.3
		}
	} else {
		cov.Gaps = append(cov.Gaps, "no observability meta")
	}
	cov.Domains["observability"] = oScore

	// network / capacity (scheduling estimate or explicit capacity meta)
	nScore := 0.0
	if base.Meta != nil {
		if _, ok := base.Meta["pod_scheduling_capacity_estimate"]; ok {
			nScore = 0.8
		} else if _, ok := base.Meta["pod_ip_capacity_available"]; ok {
			nScore = 0.8
		}
	}
	if kinds[graph.KindNode] {
		nScore = maxF(nScore, 0.4)
	}
	cov.Domains["network"] = nScore
	cov.Domains["iam"] = 0
	if !kinds[graph.KindIAMRole] && !kinds[graph.KindServiceAccount] {
		cov.Gaps = append(cov.Gaps, "IAM/IRSA not modeled — iam domain coverage 0")
	}

	// Required missing for active scenario intents
	kind := ch.EffectiveKind()
	facts := ch.Facts
	if kind == "node-failure" || kind == "node-drain" || loader.FactString(facts, "scenario", "") == "rwo-node-loss" {
		if !kinds[graph.KindPVC] {
			cov.RequiredMissing = append(cov.RequiredMissing, "pvc")
		}
		if !kinds[graph.KindNode] {
			cov.RequiredMissing = append(cov.RequiredMissing, "node")
		}
	}
	if kind == "prometheus-rule" || loader.FactString(facts, "scenario", "") == "prom-zero-match" {
		if base.Meta == nil || base.Meta["metric_labels"] == nil {
			cov.RequiredMissing = append(cov.RequiredMissing, "metric_labels")
		}
	}
	if kind == "helm-upgrade" || kind == "scale-up" || kind == "k8s-manifest" || loader.FactString(facts, "scenario", "") == "cni-ip-capacity" {
		if !hasCapacityMeta(base) {
			hasNodeCap := false
			for _, n := range base.Nodes {
				if n.Kind == graph.KindNode && n.AttrInt("allocatablePods") > 0 {
					hasNodeCap = true
					break
				}
			}
			if !hasNodeCap {
				cov.RequiredMissing = append(cov.RequiredMissing, "pod_scheduling_capacity")
			}
		}
	}

	sum := 0.0
	for _, v := range cov.Domains {
		sum += v
	}
	if len(cov.Domains) > 0 {
		cov.Overall = sum / float64(len(cov.Domains))
	}
	cov.Gaps = append(cov.Gaps, "partial graph: multi-cluster/IAM cost not fully modeled")
	_ = findings
	return cov
}

func semanticDigest(r *Report) string {
	// Clone without runtime timestamp for stable hash.
	// Includes content digests so the report is bound to exact baseline/change.
	type dig struct {
		Version            string             `json:"version"`
		ChangeID           string             `json:"changeId"`
		BaselineID         string             `json:"baselineId"`
		BaselineDigest     string             `json:"baselineDigest"`
		ChangeDigest       string             `json:"changeDigest"`
		ProposedDigest     string             `json:"proposedDigest"`
		Risk               string             `json:"risk"`
		Decision           string             `json:"decision"`
		AffectedComponents int                `json:"affected_components"`
		CriticalPaths      int                `json:"critical_paths"`
		SLOViolations      int                `json:"slo_violations"`
		Rollback           string             `json:"rollback"`
		PredictedFailures  []string           `json:"predicted_failures"`
		Findings           []scenario.Finding `json:"findings"`
		Coverage           Coverage           `json:"coverage"`
	}
	d := dig{
		Version: r.Version, ChangeID: r.ChangeID, BaselineID: r.BaselineID,
		BaselineDigest: r.BaselineDigest, ChangeDigest: r.ChangeDigest, ProposedDigest: r.ProposedDigest,
		Risk: r.Risk, Decision: r.Decision, AffectedComponents: r.AffectedComponents,
		CriticalPaths: r.CriticalPaths, SLOViolations: r.SLOViolations, Rollback: r.Rollback,
		PredictedFailures: r.PredictedFailures, Findings: r.Findings, Coverage: r.Coverage,
	}
	raw, _ := json.Marshal(d)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func digestAny(v any) (string, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	// canonical re-encode
	var anyV any
	if err := json.Unmarshal(raw, &anyV); err != nil {
		sum := sha256.Sum256(raw)
		return hex.EncodeToString(sum[:]), nil
	}
	raw2, err := json.Marshal(anyV)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw2)
	return hex.EncodeToString(sum[:]), nil
}

// AssertBindings checks that report was produced for the given baseline/change digests.
func AssertBindings(rep *Report, baselineDigest, changeDigest string) error {
	if rep == nil {
		return fmt.Errorf("nil report")
	}
	if rep.BaselineDigest == "" || rep.ChangeDigest == "" {
		return fmt.Errorf("report missing content digests (need analyze ≥1.1.0)")
	}
	if baselineDigest != "" && rep.BaselineDigest != baselineDigest {
		return fmt.Errorf("baselineDigest mismatch: report=%s live=%s", short(rep.BaselineDigest), short(baselineDigest))
	}
	if changeDigest != "" && rep.ChangeDigest != changeDigest {
		return fmt.Errorf("changeDigest mismatch: report=%s live=%s", short(rep.ChangeDigest), short(changeDigest))
	}
	return nil
}

func short(s string) string {
	if len(s) < 12 {
		return s
	}
	return s[:12]
}

func uniqueStrings(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

func maxF(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

func hasCapacityMeta(base *graph.Snapshot) bool {
	if base == nil || base.Meta == nil {
		return false
	}
	if _, ok := base.Meta["pod_scheduling_capacity_estimate"]; ok {
		return true
	}
	if _, ok := base.Meta["pod_ip_capacity_available"]; ok {
		return true
	}
	return false
}

// applyActiveCoverageGaps elevates change/baseline ingestion gaps so approve is impossible.
func applyActiveCoverageGaps(ch *loader.ChangeEnvelope, base *graph.Snapshot, cov *Coverage) {
	if ch != nil && ch.Facts != nil {
		if gap, ok := ch.Facts["coverage_gap"].(string); ok && gap != "" {
			cov.RequiredMissing = append(cov.RequiredMissing, "coverage_gap:"+gap)
			cov.Gaps = append(cov.Gaps, "change coverage_gap: "+gap)
		}
		if n := loader.FactInt(ch.Facts, "yaml_parse_errors", 0); n > 0 {
			cov.RequiredMissing = append(cov.RequiredMissing, "yaml_parse_errors")
			cov.Gaps = append(cov.Gaps, fmt.Sprintf("change yaml_parse_errors=%d", n))
		}
	}
	if base != nil && base.Meta != nil {
		if gap, ok := base.Meta["coverage_gap"].(string); ok && gap != "" {
			cov.RequiredMissing = append(cov.RequiredMissing, "baseline_coverage_gap:"+gap)
			cov.Gaps = append(cov.Gaps, "baseline coverage_gap: "+gap)
		}
		if n, ok := base.Meta["yaml_parse_errors"]; ok {
			cov.RequiredMissing = append(cov.RequiredMissing, "baseline_yaml_parse_errors")
			cov.Gaps = append(cov.Gaps, fmt.Sprintf("baseline yaml_parse_errors=%v", n))
		}
	}
}
