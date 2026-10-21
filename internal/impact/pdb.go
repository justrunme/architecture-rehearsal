package impact

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/justrunme/architecture-rehearsal/internal/graph"
	"github.com/justrunme/architecture-rehearsal/internal/podtemplate"
)

// recreateAvailabilityMsg is the fixed wording for Deployment strategy.type=Recreate.
const recreateAvailabilityMsg = "Recreate strategy terminates all pods before starting new ones; availability drops to 0 during rollout"

// PDBRollout detects rolling updates that can drive available replicas to zero,
// and compares rollout maxUnavailable against PodDisruptionBudget constraints.
//
// Important: PodDisruptionBudgets do NOT limit Deployment rolling updates; they
// only gate voluntary evictions (drain). A dangerous rollout must still BLOCK
// when this sync will actually roll pods (pod template change). If the template
// is unchanged, a dangerous existing strategy is WARN so no-op Argo syncs do
// not fail the gate.
//
// strategy.type=Recreate is treated as availability 0 (all pods terminate before
// new ones start). Template change → BLOCK; unchanged template → WARN.
type PDBRollout struct{}

func (PDBRollout) Name() string { return "pdb-rollout" }

func (d PDBRollout) Evaluate(ctx Context) Verdict {
	v := Verdict{Detector: d.Name(), Confidence: "medium"}
	if ctx.BaseIdx == nil || ctx.PropIdx == nil {
		v.Status = Unknown
		v.Explanation = "missing graph indexes"
		v.Missing = []string{"graph"}
		v.Confidence = "low"
		return v
	}

	var findings []string
	var affected []string
	worst := Pass

	pdbs := ctx.PropIdx.ByKind[graph.KindPDB]
	if len(pdbs) == 0 {
		pdbs = ctx.BaseIdx.ByKind[graph.KindPDB]
	}

	// Evaluate proposed workloads for rollouts that can leave 0 available pods.
	for _, w := range ctx.PropIdx.ByKind[graph.KindWorkload] {
		if w == nil {
			continue
		}
		rep := w.WorkloadReplicas()
		if rep <= 0 {
			continue
		}
		maxUnavailRollout, recreate := rolloutMaxUnavailable(w, rep)
		remaining := rep - maxUnavailRollout
		uncertain := syncTemplateUncertain(ctx, w)
		triggers := syncTriggersRollout(ctx, w)
		pdb := matchingPDB(ctx, pdbs, w)

		// Cannot predict template change (e.g. no last-applied): distinct reason.
		// Safe strategy → UNKNOWN; dangerous / Recreate → BLOCK (not "pods to 0" wording).
		if uncertain {
			msg := syncTemplateUncertainDetail(ctx, w)
			if msg == "" {
				msg = podtemplate.NoLastAppliedDetail
			}
			findings = append(findings, msg)
			affected = append(affected, w.ID)
			if pdb != nil {
				affected = append(affected, pdb.ID)
			}
			if remaining <= 0 || recreate {
				worst = Block
			} else if worst != Block {
				worst = Unknown
			}
			continue
		}

		if remaining > 0 {
			continue
		}
		var msg string
		if recreate {
			msg = recreateAvailabilityMsg
			affected = append(affected, w.ID)
			if pdb != nil {
				affected = append(affected, pdb.ID)
			}
			findings = append(findings, msg)
			if triggers {
				worst = Block
			} else if worst != Block {
				worst = Warn
			}
			continue
		}
		if triggers {
			if pdb != nil {
				minA := effectiveMinAvailable(pdb, rep)
				msg = fmt.Sprintf(
					"rollout can take available pods to 0; note: PDB %s (minAvailable=%d) does not protect against rollouts",
					pdb.Name, minA)
				affected = append(affected, pdb.ID, w.ID)
			} else {
				msg = fmt.Sprintf(
					"rollout can take available pods to 0 on %s (maxUnavailable=%d, replicas=%d); note: PodDisruptionBudgets do not limit Deployment rollouts (they only gate evictions)",
					display(ctx.PropIdx, w.ID), maxUnavailRollout, rep)
				affected = append(affected, w.ID)
			}
			findings = append(findings, msg)
			worst = Block
			continue
		}
		// Template unchanged: strategy is latent until the next real rollout.
		if pdb != nil {
			minA := effectiveMinAvailable(pdb, rep)
			msg = fmt.Sprintf(
				"existing strategy can take pods to 0 on next rollout; note: PDB %s (minAvailable=%d) does not protect against rollouts",
				pdb.Name, minA)
			affected = append(affected, pdb.ID, w.ID)
		} else {
			msg = fmt.Sprintf(
				"existing strategy can take pods to 0 on next rollout on %s (maxUnavailable=%d, replicas=%d)",
				display(ctx.PropIdx, w.ID), maxUnavailRollout, rep)
			affected = append(affected, w.ID)
		}
		findings = append(findings, msg)
		if worst != Block {
			worst = Warn
		}
	}

	if len(pdbs) == 0 {
		if len(findings) > 0 {
			v.Status = worst
			v.Explanation = pdbExplanation(worst, findings)
			v.Evidence = findings
			v.Affected = unique(affected)
			v.Confidence = "high"
			return v
		}
		if !ctx.PDBsCollected {
			v.Status = Unknown
			v.Explanation = "PDB inventory status unknown (baseline did not record PDB collection)"
			v.Missing = []string{"pdb-inventory"}
			v.Confidence = "low"
			return v
		}
		v.Status = Pass
		v.Explanation = "no PodDisruptionBudgets in graph"
		v.Confidence = "high"
		return v
	}

	// Need PROTECTED_BY edges or matchLabels to bind PDBs to workloads.
	hasLink := false
	for _, e := range append(edgesOf(ctx.Proposed), edgesOf(ctx.Baseline)...) {
		if e.Rel == graph.RelProtectedBy {
			hasLink = true
			break
		}
	}
	for _, pdb := range pdbs {
		if _, ok := pdb.Attributes["matchLabels"]; ok {
			hasLink = true
			break
		}
	}
	if !hasLink {
		if len(findings) > 0 {
			v.Status = worst
			v.Explanation = pdbExplanation(worst, findings)
			v.Evidence = findings
			v.Affected = unique(affected)
			v.Confidence = "high"
			return v
		}
		v.Status = Unknown
		v.Explanation = "PDBs present but no PROTECTED_BY edges or matchLabels to bind workloads"
		v.Missing = []string{"pdb-workload-link"}
		v.Confidence = "low"
		return v
	}

	for _, pdb := range pdbs {
		workloads := workloadsForPDB(ctx, pdb)
		for _, wid := range workloads {
			w := ctx.PropIdx.ByID[wid]
			if w == nil {
				w = ctx.BaseIdx.ByID[wid]
			}
			if w == nil {
				v.Status = Unknown
				v.Explanation = fmt.Sprintf("PDB %s references unresolved workload %s", pdb.Name, wid)
				v.Missing = []string{"workload:" + wid}
				v.Confidence = "low"
				return v
			}
			rep := w.WorkloadReplicas()
			if rep <= 0 {
				v.Status = Unknown
				v.Explanation = fmt.Sprintf("PDB %s workload %s has replicas<=0 (unknown rollout impact)", pdb.Name, display(ctx.PropIdx, wid))
				v.Missing = []string{"replicas:" + wid}
				v.Confidence = "low"
				return v
			}
			minA := effectiveMinAvailable(pdb, rep)
			maxU := effectiveMaxUnavailable(pdb, rep)

			maxUnavailRollout, recreate := rolloutMaxUnavailable(w, rep)
			remaining := rep - maxUnavailRollout

			// Zero-available / Recreate / uncertain already reported above.
			if remaining <= 0 || recreate || syncTemplateUncertain(ctx, w) {
				continue
			}

			triggers := syncTriggersRollout(ctx, w)

			// Remaining > 0 but below declared minAvailable: PDB states availability
			// intent — falling below it during a triggered rollout is BLOCK.
			if minA > 0 && remaining < minA {
				var msg string
				if triggers {
					msg = fmt.Sprintf(
						"rollout can drop available pods to %d, below the minAvailable=%d your PDB declares (PDBs do not enforce this during rollouts)",
						remaining, minA)
					findings = append(findings, msg)
					affected = append(affected, pdb.ID, wid)
					worst = Block
				} else {
					msg = fmt.Sprintf(
						"existing strategy can drop available pods to %d, below the minAvailable=%d your PDB declares (PDBs do not enforce this during rollouts)",
						remaining, minA)
					findings = append(findings, msg)
					affected = append(affected, pdb.ID, wid)
					if worst != Block && worst != Unknown {
						worst = Warn
					}
				}
			}
			// PDB maxUnavailable stricter than rollout — warn when conflicting.
			if maxU > 0 && maxUnavailRollout > maxU {
				msg := fmt.Sprintf(
					"PDB %s maxUnavailable=%d tighter than rollout maxUnavailable=%d on %s; note: PodDisruptionBudgets do not limit Deployment rollouts (they only gate evictions)",
					pdb.Name, maxU, maxUnavailRollout, display(ctx.PropIdx, wid))
				findings = append(findings, msg)
				affected = append(affected, pdb.ID, wid)
				if worst != Block && worst != Unknown {
					worst = Warn
				}
			}
			// Single replica with minAvailable=1 blocks any disruption (eviction), not rollouts.
			if rep == 1 && minA >= 1 {
				msg := fmt.Sprintf(
					"PDB %s minAvailable=%d with single-replica workload %s blocks eviction disruption (not rollouts)",
					pdb.Name, minA, display(ctx.PropIdx, wid))
				findings = append(findings, msg)
				affected = append(affected, pdb.ID, wid)
				if worst != Block && worst != Unknown {
					worst = Warn
				}
			}
		}
	}

	if len(findings) == 0 {
		v.Status = Pass
		v.Explanation = "rollout respects availability constraints (PDB noted for eviction only)"
		v.Confidence = "high"
		return v
	}
	v.Status = worst
	v.Explanation = pdbExplanation(worst, findings)
	v.Evidence = findings
	v.Affected = unique(affected)
	v.Confidence = "high"
	return v
}

func pdbExplanation(worst string, findings []string) string {
	switch {
	case hasRecreateFinding(findings):
		return recreateAvailabilityMsg
	case hasNoLastAppliedFinding(findings):
		return podtemplate.NoLastAppliedDetail
	case worst == Block && hasZeroAvailabilityFinding(findings):
		return "rollout can take available pods to 0; note: PodDisruptionBudgets do not protect against rollouts"
	case worst == Block && hasBelowMinAvailableFinding(findings):
		return "rollout can drop available pods below the minAvailable your PDB declares (PDBs do not enforce this during rollouts)"
	case worst == Warn && hasExistingStrategyFinding(findings):
		return "existing strategy can take pods to 0 on next rollout"
	case worst == Unknown:
		return podtemplate.NoLastAppliedDetail
	case worst == Block:
		return "rollout can drop available pods below PDB minAvailable; note: PodDisruptionBudgets do not limit Deployment rollouts (they only gate evictions)"
	default:
		return "rollout may conflict with PodDisruptionBudget constraints; note: PDBs only gate evictions"
	}
}

func hasZeroAvailabilityFinding(findings []string) bool {
	for _, f := range findings {
		if strings.Contains(f, "take available pods to 0") || strings.Contains(f, "take pods to 0") {
			return true
		}
	}
	return false
}

func hasBelowMinAvailableFinding(findings []string) bool {
	for _, f := range findings {
		if strings.Contains(f, "below the minAvailable=") {
			return true
		}
	}
	return false
}

func hasExistingStrategyFinding(findings []string) bool {
	for _, f := range findings {
		if strings.Contains(f, "existing strategy") {
			return true
		}
	}
	return false
}

func hasRecreateFinding(findings []string) bool {
	for _, f := range findings {
		if strings.Contains(f, recreateAvailabilityMsg) {
			return true
		}
	}
	return false
}

func hasNoLastAppliedFinding(findings []string) bool {
	for _, f := range findings {
		if strings.Contains(f, "no last-applied-configuration") {
			return true
		}
	}
	return false
}

// rolloutMaxUnavailable returns how many pods can be unavailable during this
// workload's update strategy. Recreate → all replicas (availability 0).
func rolloutMaxUnavailable(w *graph.Node, replicas int) (maxUnavail int, recreate bool) {
	if w != nil && strings.EqualFold(w.AttrString("strategyType"), "Recreate") {
		return replicas, true
	}
	return parseMaxUnavailable(w.AttrString("maxUnavailable"), replicas), false
}

// syncTemplateUncertain is true when live compare could not predict removals
// (no last-applied / SSA without SSA mode) and failed closed.
func syncTemplateUncertain(ctx Context, w *graph.Node) bool {
	if w == nil || ctx.Change == nil {
		return false
	}
	for _, n := range append(ctx.Change.AddedNodes, ctx.Change.PatchNodes...) {
		if n.ID != w.ID {
			continue
		}
		if b, ok := n.Attributes["podTemplateUncertain"].(bool); ok && b {
			return true
		}
		detail, _ := n.Attributes["podTemplateCompareDetail"].(string)
		if strings.Contains(detail, "no last-applied-configuration") {
			return true
		}
	}
	return false
}

func syncTemplateUncertainDetail(ctx Context, w *graph.Node) string {
	if w == nil || ctx.Change == nil {
		return ""
	}
	for _, n := range append(ctx.Change.AddedNodes, ctx.Change.PatchNodes...) {
		if n.ID != w.ID {
			continue
		}
		if detail, ok := n.Attributes["podTemplateCompareDetail"].(string); ok && detail != "" {
			return detail
		}
	}
	return ""
}

// syncTriggersRollout is true when this sync will roll pods for workload w.
// Preference order:
//  1. Explicit podTemplateChanged on an Added/Patch node for this workload
//  2. Subset compare of desired vs live podTemplate attrs (fail-closed if live missing)
//  3. Workload not touched by the change → false (no rollout from this sync)
func syncTriggersRollout(ctx Context, w *graph.Node) bool {
	if w == nil {
		return false
	}
	if ctx.Change != nil {
		for _, a := range ctx.Change.AddedNodes {
			if a.ID == w.ID {
				if v, ok := a.Attributes["podTemplateChanged"].(bool); ok {
					return v
				}
				return true
			}
		}
		for _, p := range ctx.Change.PatchNodes {
			if p.ID != w.ID {
				continue
			}
			if v, ok := p.Attributes["podTemplateChanged"].(bool); ok {
				return v
			}
			// Patch without explicit flag: subset-compare templates if present.
			desiredTpl, _ := p.Attributes["podTemplate"].(map[string]any)
			if desiredTpl == nil {
				desiredTpl, _ = w.Attributes["podTemplate"].(map[string]any)
			}
			var liveTpl map[string]any
			if ctx.BaseIdx != nil {
				if base := ctx.BaseIdx.ByID[w.ID]; base != nil {
					liveTpl, _ = base.Attributes["podTemplate"].(map[string]any)
				}
			}
			return podtemplate.SubsetChanged(desiredTpl, liveTpl)
		}
		// Not part of this sync → strategy may be dangerous but pods will not roll.
		return false
	}
	// No change envelope: fail closed.
	return true
}

func matchingPDB(ctx Context, pdbs []*graph.Node, w *graph.Node) *graph.Node {
	if w == nil {
		return nil
	}
	for _, pdb := range pdbs {
		for _, wid := range workloadsForPDB(ctx, pdb) {
			if wid == w.ID {
				return pdb
			}
		}
	}
	return nil
}

func edgesOf(s *graph.Snapshot) []graph.Edge {
	if s == nil {
		return nil
	}
	return s.Edges
}

func workloadsForPDB(ctx Context, pdb *graph.Node) []string {
	var out []string
	seen := map[string]bool{}
	// PROTECTED_BY: workload → pdb
	idx := ctx.PropIdx
	if idx == nil {
		idx = ctx.BaseIdx
	}
	for wid, edges := range idx.Out {
		for _, e := range edges {
			if e.To == pdb.ID && e.Rel == graph.RelProtectedBy {
				if !seen[wid] {
					seen[wid] = true
					out = append(out, wid)
				}
			}
		}
	}
	if ml, ok := pdb.Attributes["matchLabels"].(map[string]any); ok {
		for _, w := range idx.ByKind[graph.KindWorkload] {
			if w.Namespace != pdb.Namespace {
				continue
			}
			labels, _ := w.Attributes["podLabels"].(map[string]any)
			if selectorMatches(ml, labels) {
				if !seen[w.ID] {
					seen[w.ID] = true
					out = append(out, w.ID)
				}
			}
		}
	}
	return out
}

func effectiveMinAvailable(pdb *graph.Node, replicas int) int {
	if pdb == nil {
		return 0
	}
	if pct := pdb.AttrInt("minAvailablePercent"); pct > 0 && replicas > 0 {
		return int(math.Ceil(float64(replicas) * float64(pct) / 100.0))
	}
	if n := pdb.AttrInt("minAvailable"); n > 0 {
		return n
	}
	return 0
}

func effectiveMaxUnavailable(pdb *graph.Node, replicas int) int {
	if pdb == nil {
		return 0
	}
	if pct := pdb.AttrInt("maxUnavailablePercent"); pct > 0 && replicas > 0 {
		return int(math.Floor(float64(replicas) * float64(pct) / 100.0))
	}
	if n := pdb.AttrInt("maxUnavailable"); n > 0 {
		return n
	}
	return 0
}

// parseMaxUnavailable follows Kubernetes RollingUpdate semantics: percentage
// values round DOWN; the default when unset is 25% (so 25% of 3 replicas → 0).
func parseMaxUnavailable(ms string, replicas int) int {
	if ms == "" {
		return int(math.Floor(float64(replicas) * 0.25))
	}
	if strings.HasSuffix(ms, "%") {
		p, _ := strconv.Atoi(strings.TrimSuffix(ms, "%"))
		return int(math.Floor(float64(replicas) * float64(p) / 100.0))
	}
	v, _ := strconv.Atoi(ms)
	if v < 0 {
		return 0
	}
	return v
}
