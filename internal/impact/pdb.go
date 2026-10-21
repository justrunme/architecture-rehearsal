package impact

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/justrunme/architecture-rehearsal/internal/graph"
)

// PDBRollout detects rolling updates that can drive available replicas to zero,
// and compares rollout maxUnavailable against PodDisruptionBudget constraints.
//
// Important: PodDisruptionBudgets do NOT limit Deployment rolling updates; they
// only gate voluntary evictions (drain). A dangerous rollout must still BLOCK
// even when no PDB exists.
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

	// Always evaluate proposed workloads for rollouts that can leave 0 available
	// pods — with or without a PDB.
	for _, w := range ctx.PropIdx.ByKind[graph.KindWorkload] {
		if w == nil {
			continue
		}
		rep := w.WorkloadReplicas()
		if rep <= 0 {
			continue
		}
		maxUnavailRollout := parseMaxUnavailable(w.AttrString("maxUnavailable"), rep)
		remaining := rep - maxUnavailRollout
		if remaining > 0 {
			continue
		}
		pdb := matchingPDB(ctx, pdbs, w)
		var msg string
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
	}

	if len(pdbs) == 0 {
		if len(findings) > 0 {
			v.Status = Block
			v.Explanation = "rollout can take available pods to 0; note: PodDisruptionBudgets do not limit Deployment rollouts (they only gate evictions)"
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
			v.Status = Block
			v.Explanation = "rollout can take available pods to 0; note: PodDisruptionBudgets do not limit Deployment rollouts (they only gate evictions)"
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

			maxUnavailRollout := parseMaxUnavailable(w.AttrString("maxUnavailable"), rep)
			remaining := rep - maxUnavailRollout

			// Already reported as zero-available above; still ensure PDB note is present.
			if remaining <= 0 {
				continue
			}

			// Violation: rollout could drop below declared minAvailable.
			// PDB does not actually stop the rollout — call that out plainly.
			if minA > 0 && remaining < minA {
				msg := fmt.Sprintf(
					"PDB %s minAvailable=%d; rollout maxUnavailable=%d on %s leaves %d available; note: PodDisruptionBudgets do not protect against rollouts",
					pdb.Name, minA, maxUnavailRollout, display(ctx.PropIdx, wid), remaining)
				findings = append(findings, msg)
				affected = append(affected, pdb.ID, wid)
				worst = Block
			}
			// PDB maxUnavailable stricter than rollout — warn when conflicting.
			if maxU > 0 && maxUnavailRollout > maxU {
				msg := fmt.Sprintf(
					"PDB %s maxUnavailable=%d tighter than rollout maxUnavailable=%d on %s; note: PodDisruptionBudgets do not limit Deployment rollouts (they only gate evictions)",
					pdb.Name, maxU, maxUnavailRollout, display(ctx.PropIdx, wid))
				findings = append(findings, msg)
				affected = append(affected, pdb.ID, wid)
				if worst != Block {
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
				if worst != Block {
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
	if worst == Block {
		if hasZeroAvailabilityFinding(findings) {
			v.Explanation = "rollout can take available pods to 0; note: PodDisruptionBudgets do not protect against rollouts"
		} else {
			v.Explanation = "rollout can drop available pods below PDB minAvailable; note: PodDisruptionBudgets do not limit Deployment rollouts (they only gate evictions)"
		}
	} else {
		v.Explanation = "rollout may conflict with PodDisruptionBudget constraints; note: PDBs only gate evictions"
	}
	v.Evidence = findings
	v.Affected = unique(affected)
	v.Confidence = "high"
	return v
}

func hasZeroAvailabilityFinding(findings []string) bool {
	for _, f := range findings {
		if strings.Contains(f, "take available pods to 0") {
			return true
		}
	}
	return false
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
