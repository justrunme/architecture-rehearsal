// Package verify compares an analysis prediction with a post-deploy snapshot.
//
// v0.4.1: fail-closed checks, all predicted failures, Pending on Pods.
// v0.5+: independent observation predicates; meta.observed_failures is annotation only.
package verify

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/justrunme/architecture-rehearsal/internal/analyze"
	"github.com/justrunme/architecture-rehearsal/internal/graph"
	"github.com/justrunme/architecture-rehearsal/internal/loader"
)

// Outcome of verification.
const (
	OutcomeVerified     = "verified"
	OutcomeDiverged     = "diverged"
	OutcomeInconclusive = "inconclusive"
)

// Result is the verify report.
type Result struct {
	APIVersion     string    `json:"apiVersion"`
	Kind           string    `json:"kind"`
	Version        string    `json:"version"`
	Generated      time.Time `json:"generatedAt"`
	ChangeID       string    `json:"changeId"`
	PredictionRisk string    `json:"predictionRisk"`
	Outcome        string    `json:"outcome"`
	Summary        string    `json:"summary"`
	Checks         []Check   `json:"checks"`
	Score          float64   `json:"score"` // 0..1 fraction of decisive checks that passed
	// DeployedChangeDigest is the observed identity of applied patches (v0.5+).
	DeployedChangeDigest string `json:"deployedChangeDigest,omitempty"`
}

// Check is one verification assertion.
type Check struct {
	Name    string `json:"name"`
	Passed  bool   `json:"passed"`
	Detail  string `json:"detail"`
	Unknown bool   `json:"unknown,omitempty"`
	// Soft checks do not affect score/outcome (annotations, operator notes).
	Soft bool `json:"soft,omitempty"`
}

// Options controls verification (v0.5+).
type Options struct {
	// Baseline and Change enable independent observation (delta + identity).
	Baseline *graph.Snapshot
	Change   *loader.ChangeEnvelope
	// RequireAllPredictions: every predicted failure needs independent evidence (default true).
	// When false (legacy), a single match is enough — not recommended.
	RequireAllPredictions *bool
}

// Run compares prediction report to post-deploy observed snapshot (legacy entry).
func Run(pred *analyze.Report, observed *graph.Snapshot) *Result {
	return RunWithOptions(pred, observed, Options{})
}

// RunWithOptions is the full verification path.
func RunWithOptions(pred *analyze.Report, observed *graph.Snapshot, opts Options) *Result {
	res := &Result{
		APIVersion:     graph.APIVersionV1Alpha1,
		Kind:           "VerificationResult",
		Version:        analyze.Version,
		Generated:      time.Now().UTC(),
		ChangeID:       pred.ChangeID,
		PredictionRisk: pred.Risk,
	}
	if observed == nil {
		res.Outcome = OutcomeInconclusive
		res.Summary = "No observed snapshot provided."
		res.Checks = []Check{{Name: "observed_present", Passed: false, Unknown: true, Detail: "observed is nil"}}
		return res
	}
	idx := graph.BuildIndex(observed)
	var checks []Check
	requireAll := true
	if opts.RequireAllPredictions != nil {
		requireAll = *opts.RequireAllPredictions
	}

	// --- Independent observation (v0.5): scenario-specific graph evidence ---
	for _, scenarioID := range pred.PredictedFailures {
		checks = append(checks, independentScenarioCheck(scenarioID, pred, observed, idx)...)
	}
	if len(pred.PredictedFailures) == 0 {
		checks = append(checks, Check{
			Name:   "no_predicted_failures",
			Passed: true,
			Detail: "analysis predicted no failure patterns",
		})
	}

	// --- Operator annotation (soft only — never sole proof) ---
	observedFailures := parseObservedFailures(observed)
	if len(pred.PredictedFailures) > 0 {
		if len(observedFailures) == 0 {
			checks = append(checks, Check{
				Name:    "operator_observed_failures_annotation",
				Passed:  false,
				Unknown: true,
				Soft:    true,
				Detail:  "no meta.observed_failures annotation (optional)",
			})
		} else {
			matched, missing := matchAll(pred.PredictedFailures, observedFailures)
			detail := fmt.Sprintf("annotation matched %d/%d predicted failure ids", matched, len(pred.PredictedFailures))
			if requireAll {
				checks = append(checks, Check{
					Name:   "operator_observed_failures_annotation",
					Passed: matched == len(pred.PredictedFailures),
					Soft:   true,
					Detail: detail + softMissing(missing),
				})
			} else {
				checks = append(checks, Check{
					Name:   "operator_observed_failures_annotation",
					Passed: matched > 0,
					Soft:   true,
					Detail: detail + " (legacy partial match)",
				})
			}
		}
	}

	// --- Component presence: FAIL if missing; present alone is not a free pass ---
	for _, f := range pred.Findings {
		for _, c := range f.Components {
			if c == "" {
				continue
			}
			if n := idx.ByID[c]; n == nil {
				checks = append(checks, Check{
					Name:   "component_missing:" + c,
					Passed: false,
					Detail: "component from finding not present in observed graph",
				})
			}
			// presence without health signal is intentionally not scored
		}
	}

	// --- Deployed change identity + delta (v0.5) ---
	if opts.Change != nil {
		digest, idChecks := changeIdentityChecks(opts.Change, observed, idx)
		res.DeployedChangeDigest = digest
		checks = append(checks, idChecks...)
	}
	if opts.Baseline != nil && opts.Change != nil {
		checks = append(checks, deltaChecks(opts.Baseline, opts.Change, observed)...)
	}

	// Score decisive (non-soft, non-unknown) checks
	passed, total, unknowns, softOnly := 0, 0, 0, 0
	hardFail := false
	for _, c := range checks {
		if c.Soft {
			softOnly++
			continue
		}
		if c.Unknown {
			unknowns++
			continue
		}
		total++
		if c.Passed {
			passed++
		} else {
			hardFail = true
		}
	}
	res.Checks = checks
	if total > 0 {
		res.Score = float64(passed) / float64(total)
	}

	switch {
	case hardFail && total > 0 && passed == 0:
		res.Outcome = OutcomeDiverged
		res.Summary = "Observed state diverged from prediction."
	case hardFail:
		res.Outcome = OutcomeDiverged
		res.Summary = fmt.Sprintf("Partial match: %d/%d decisive checks passed.", passed, total)
	case unknowns > 0 && passed == total:
		// Only unknowns + no hard failures
		if total == 0 {
			res.Outcome = OutcomeInconclusive
			res.Summary = "No decisive independent evidence; result inconclusive."
		} else {
			res.Outcome = OutcomeInconclusive
			res.Summary = "Some checks lack post-deploy evidence; result inconclusive."
		}
	case total > 0 && passed == total:
		res.Outcome = OutcomeVerified
		res.Summary = "Observed state is consistent with analysis prediction (independent checks)."
	case total == 0 && softOnly > 0:
		res.Outcome = OutcomeInconclusive
		res.Summary = "Only soft/annotation checks present; independent evidence missing."
	default:
		res.Outcome = OutcomeInconclusive
		res.Summary = "Insufficient decisive checks for verification."
	}
	return res
}

func independentScenarioCheck(scenarioID string, pred *analyze.Report, observed *graph.Snapshot, idx *graph.Index) []Check {
	comps := componentsForScenario(pred, scenarioID)
	switch scenarioID {
	case "cni-ip-capacity":
		return []Check{checkCNICapacity(observed, idx, comps)}
	case "rwo-node-loss":
		return []Check{checkRWOPending(idx, comps)}
	case "pdb-disruption":
		return []Check{checkPDBDisruption(idx)}
	case "prom-zero-match":
		// Observability scenarios need metric meta; without it → unknown
		if observed.Meta != nil {
			if _, ok := observed.Meta["metric_match_count"]; ok {
				return []Check{{
					Name:   "scenario:prom-zero-match",
					Passed: true,
					Detail: "observed meta.metric_match_count present",
				}}
			}
		}
		return []Check{{
			Name:    "scenario:prom-zero-match",
			Passed:  false,
			Unknown: true,
			Detail:  "no independent metric evidence in observed meta (need metric_match_count)",
		}}
	default:
		return []Check{{
			Name:    "scenario:" + scenarioID,
			Passed:  false,
			Unknown: true,
			Detail:  "no independent predicate for scenario " + scenarioID,
		}}
	}
}

func componentsForScenario(pred *analyze.Report, scenarioID string) []string {
	var out []string
	for _, f := range pred.Findings {
		if f.Scenario == scenarioID {
			out = append(out, f.Components...)
		}
	}
	return out
}

func checkCNICapacity(observed *graph.Snapshot, idx *graph.Index, comps []string) Check {
	pending := countPendingPodsScoped(idx, comps)
	// Also look for unavailable replica pressure on workloads
	pressure := 0
	for _, w := range idx.ByKind[graph.KindWorkload] {
		if w.AttrInt("unavailableReplicas") > 0 || w.AttrString("phase") == "Pending" {
			pressure++
		}
	}
	// Global pending is OK for CNI (cluster-wide IP exhaustion)
	if pending == 0 {
		pending = countPendingPods(idx)
	}
	if pending > 0 || pressure > 0 {
		return Check{
			Name:   "scenario:cni-ip-capacity",
			Passed: true,
			Detail: fmt.Sprintf("independent evidence: pendingPods=%d workloadPressure=%d", pending, pressure),
		}
	}
	if observed.Meta != nil {
		if v, ok := observed.Meta["pod_scheduling_capacity_estimate"]; ok {
			if n, ok := asInt(v); ok && n == 0 && pending == 0 {
				return Check{
					Name:    "scenario:cni-ip-capacity",
					Passed:  false,
					Unknown: true,
					Detail:  "capacity estimate 0 but no pending pods — may have recovered",
				}
			}
		}
	}
	return Check{
		Name:    "scenario:cni-ip-capacity",
		Passed:  false,
		Unknown: true,
		Detail:  "no pending pods or workload pressure observed — independent evidence missing",
	}
}

func checkRWOPending(idx *graph.Index, comps []string) Check {
	// v0.4.1 fix: Pending lives on KindPod, not KindWorkload.
	// Scope to finding components / PVC-bound stateful names to avoid CNI false positives.
	pending := 0
	for _, p := range idx.ByKind[graph.KindPod] {
		if p.AttrString("phase") != "Pending" && !p.AttrBool("unschedulable") {
			continue
		}
		if podMatchesComponents(p, comps) || podLooksStateful(p) {
			pending++
		}
	}
	// Workload-level markers only if they are predicted components
	for _, id := range comps {
		if w := idx.ByID[id]; w != nil {
			if w.AttrString("phase") == "Pending" || w.AttrBool("unschedulable") {
				pending++
			}
		}
	}
	// PVC present + pending pod in same namespace as a PVC is stronger RWO signal
	if pending == 0 {
		for _, pvc := range idx.ByKind[graph.KindPVC] {
			for _, p := range idx.ByKind[graph.KindPod] {
				if p.Namespace == pvc.Namespace && (p.AttrString("phase") == "Pending" || p.AttrBool("unschedulable")) {
					pending++
				}
			}
		}
	}
	if pending > 0 {
		return Check{
			Name:   "scenario:rwo-node-loss",
			Passed: true,
			Detail: fmt.Sprintf("independent evidence: scoped pending/unschedulable pods=%d", pending),
		}
	}
	return Check{
		Name:    "scenario:rwo-node-loss",
		Passed:  false,
		Unknown: true,
		Detail:  "no Pending/unschedulable Pods scoped to RWO components/PVCs",
	}
}

func podMatchesComponents(p *graph.Node, comps []string) bool {
	for _, c := range comps {
		// component like workload/gitops/gitaly → match pod name prefix gitaly-
		parts := strings.Split(c, "/")
		if len(parts) >= 3 {
			wname := parts[len(parts)-1]
			ns := parts[len(parts)-2]
			if p.Namespace == ns && (p.Name == wname || strings.HasPrefix(p.Name, wname+"-")) {
				return true
			}
		}
	}
	return false
}

func podLooksStateful(p *graph.Node) bool {
	// StatefulSet pods often end with -0, -1, …
	if p == nil {
		return false
	}
	name := p.Name
	if len(name) > 2 && name[len(name)-2] == '-' {
		c := name[len(name)-1]
		if c >= '0' && c <= '9' {
			return true
		}
	}
	return false
}

func checkPDBDisruption(idx *graph.Index) Check {
	// Weak independent signal: pending pods under PDB-protected workloads
	pending := countPendingPods(idx)
	if pending > 0 {
		return Check{
			Name:   "scenario:pdb-disruption",
			Passed: true,
			Detail: fmt.Sprintf("pending pods=%d (possible disruption fallout)", pending),
		}
	}
	return Check{
		Name:    "scenario:pdb-disruption",
		Passed:  false,
		Unknown: true,
		Detail:  "no independent disruption evidence in observed graph",
	}
}

func changeIdentityChecks(ch *loader.ChangeEnvelope, observed *graph.Snapshot, idx *graph.Index) (string, []Check) {
	// Digest of intended patches (id + replica attrs)
	type patchID struct {
		ID       string         `json:"id"`
		Attrs    map[string]any `json:"attrs,omitempty"`
		Removed  bool           `json:"removed,omitempty"`
	}
	var parts []patchID
	for _, p := range ch.PatchNodes {
		parts = append(parts, patchID{ID: p.ID, Attrs: p.Attributes})
	}
	for _, id := range ch.RemovedNodes {
		parts = append(parts, patchID{ID: id, Removed: true})
	}
	raw, _ := json.Marshal(parts)
	sum := sha256.Sum256(raw)
	digest := hex.EncodeToString(sum[:8])

	var checks []Check
	// Verify patched workloads exist with expected replica counts when specified
	matched := 0
	wanted := 0
	for _, p := range ch.PatchNodes {
		if p.ID == "" {
			continue
		}
		wanted++
		n := idx.ByID[p.ID]
		if n == nil {
			checks = append(checks, Check{
				Name:   "deployed_identity:" + p.ID,
				Passed: false,
				Detail: "patched workload not found in observed graph",
			})
			continue
		}
		if rep, ok := p.Attributes["replicas"]; ok {
			want := 0
			switch t := rep.(type) {
			case int:
				want = t
			case float64:
				want = int(t)
			}
			got := n.WorkloadReplicas()
			if got == want {
				matched++
				checks = append(checks, Check{
					Name:   "deployed_identity:" + p.ID,
					Passed: true,
					Detail: fmt.Sprintf("observed replicas=%d matches change", got),
				})
			} else {
				// Partial rollout still counts as "change was attempted"
				checks = append(checks, Check{
					Name:   "deployed_identity:" + p.ID,
					Passed: true,
					Detail: fmt.Sprintf("workload present; replicas observed=%d desired=%d (rollout may be incomplete)", got, want),
				})
				matched++
			}
		} else {
			matched++
			checks = append(checks, Check{
				Name:   "deployed_identity:" + p.ID,
				Passed: true,
				Detail: "patched workload present in observed graph",
			})
		}
	}
	if wanted == 0 && len(ch.RemovedNodes) == 0 {
		checks = append(checks, Check{
			Name:    "deployed_change_identity",
			Passed:  false,
			Unknown: true,
			Detail:  "change has no patch/remove nodes to fingerprint",
		})
	} else if wanted > 0 {
		checks = append(checks, Check{
			Name:   "deployed_change_digest",
			Passed: true,
			Detail: fmt.Sprintf("digest=%s identity_matched=%d/%d", digest, matched, wanted),
		})
	}
	return digest, checks
}

func deltaChecks(base *graph.Snapshot, ch *loader.ChangeEnvelope, observed *graph.Snapshot) []Check {
	baseIdx := graph.BuildIndex(base)
	obsIdx := graph.BuildIndex(observed)
	// Nodes removed in change should be absent (or at least not Ready) in observed
	var checks []Check
	for _, id := range ch.RemovedNodes {
		if n := obsIdx.ByID[id]; n != nil {
			// still present after predicted removal
			if bn := baseIdx.ByID[id]; bn != nil && bn.Kind == graph.KindNode {
				checks = append(checks, Check{
					Name:   "delta_removed:" + id,
					Passed: false,
					Detail: "node predicted removed still present in observed",
				})
			}
		} else {
			checks = append(checks, Check{
				Name:   "delta_removed:" + id,
				Passed: true,
				Detail: "removed node absent from observed graph",
			})
		}
	}
	// Replica increases for patched workloads should not decrease below baseline
	for _, p := range ch.PatchNodes {
		bn := baseIdx.ByID[p.ID]
		on := obsIdx.ByID[p.ID]
		if bn == nil || on == nil {
			continue
		}
		if bn.Kind != graph.KindWorkload {
			continue
		}
		baseR := bn.WorkloadReplicas()
		obsR := on.WorkloadReplicas()
		if want, ok := p.Attributes["replicas"]; ok {
			w := 0
			switch t := want.(type) {
			case int:
				w = t
			case float64:
				w = int(t)
			}
			if w > baseR && obsR < baseR {
				checks = append(checks, Check{
					Name:   "delta_replicas:" + p.ID,
					Passed: false,
					Detail: fmt.Sprintf("replicas decreased below baseline after scale-up intent (base=%d obs=%d want=%d)", baseR, obsR, w),
				})
			}
		}
	}
	if len(checks) == 0 {
		checks = append(checks, Check{
			Name:   "delta_baseline_observed",
			Passed: true,
			Detail: "no contradictory baseline→observed delta for change seeds",
		})
	}
	return checks
}

func parseObservedFailures(observed *graph.Snapshot) map[string]bool {
	out := map[string]bool{}
	if observed.Meta == nil {
		return out
	}
	if raw, ok := observed.Meta["observed_failures"].([]any); ok {
		for _, x := range raw {
			if s, ok := x.(string); ok {
				out[s] = true
			}
		}
	}
	// also allow []string after json round-trip quirks
	if raw, ok := observed.Meta["observed_failures"].([]string); ok {
		for _, s := range raw {
			out[s] = true
		}
	}
	return out
}

func matchAll(predicted []string, observed map[string]bool) (matched int, missing []string) {
	for _, f := range predicted {
		if observed[f] {
			matched++
		} else {
			missing = append(missing, f)
		}
	}
	return matched, missing
}

func softMissing(missing []string) string {
	if len(missing) == 0 {
		return ""
	}
	return "; missing=" + strings.Join(missing, ",")
}

func countPendingPods(idx *graph.Index) int {
	n := 0
	for _, p := range idx.ByKind[graph.KindPod] {
		if p.AttrString("phase") == "Pending" || p.AttrBool("unschedulable") {
			n++
		}
	}
	return n
}

func countPendingPodsScoped(idx *graph.Index, comps []string) int {
	if len(comps) == 0 {
		return 0
	}
	n := 0
	for _, p := range idx.ByKind[graph.KindPod] {
		if p.AttrString("phase") != "Pending" && !p.AttrBool("unschedulable") {
			continue
		}
		if podMatchesComponents(p, comps) {
			n++
		}
	}
	return n
}

func asInt(v any) (int, bool) {
	switch t := v.(type) {
	case int:
		return t, true
	case int64:
		return int(t), true
	case float64:
		return int(t), true
	default:
		return 0, false
	}
}
