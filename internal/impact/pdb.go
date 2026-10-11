package impact

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/justrunme/architecture-rehearsal/internal/graph"
)

// PDBRollout detects PodDisruptionBudget violations caused by a rolling sync/rollout.
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

	pdbs := ctx.PropIdx.ByKind[graph.KindPDB]
	if len(pdbs) == 0 {
		pdbs = ctx.BaseIdx.ByKind[graph.KindPDB]
	}
	if len(pdbs) == 0 {
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
		v.Status = Unknown
		v.Explanation = "PDBs present but no PROTECTED_BY edges or matchLabels to bind workloads"
		v.Missing = []string{"pdb-workload-link"}
		v.Confidence = "low"
		return v
	}

	var findings []string
	var affected []string
	worst := Pass

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

			// Rolling update surge/unavailable from workload strategy.
			maxUnavailRollout := parseMaxUnavailable(w.AttrString("maxUnavailable"), rep)
			if maxUnavailRollout == 0 {
				// Default RollingUpdate maxUnavailable is 25%.
				maxUnavailRollout = int(math.Max(1, math.Ceil(float64(rep)*0.25)))
			}

			// Violation: rollout could drop below minAvailable.
			if minA > 0 {
				remaining := rep - maxUnavailRollout
				if remaining < minA {
					msg := fmt.Sprintf(
						"PDB %s minAvailable=%d; rollout maxUnavailable=%d on %s leaves %d available",
						pdb.Name, minA, maxUnavailRollout, display(ctx.PropIdx, wid), remaining)
					findings = append(findings, msg)
					affected = append(affected, pdb.ID, wid)
					worst = Block
				}
			}
			// PDB maxUnavailable stricter than rollout — warn when equal-tight or conflicting.
			if maxU > 0 && maxUnavailRollout > maxU {
				msg := fmt.Sprintf(
					"PDB %s maxUnavailable=%d tighter than rollout maxUnavailable=%d on %s",
					pdb.Name, maxU, maxUnavailRollout, display(ctx.PropIdx, wid))
				findings = append(findings, msg)
				affected = append(affected, pdb.ID, wid)
				if worst != Block {
					worst = Warn
				}
			}
			// Single replica with minAvailable=1 blocks any disruption.
			if rep == 1 && minA >= 1 {
				msg := fmt.Sprintf(
					"PDB %s minAvailable=%d with single-replica workload %s blocks rollout disruption",
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
		v.Explanation = "rollout respects PodDisruptionBudget constraints"
		v.Confidence = "high"
		return v
	}
	v.Status = worst
	if worst == Block {
		v.Explanation = "rollout would violate PodDisruptionBudget (maxUnavailable vs minAvailable)"
	} else {
		v.Explanation = "rollout may conflict with PodDisruptionBudget constraints"
	}
	v.Evidence = findings
	v.Affected = unique(affected)
	v.Confidence = "high"
	return v
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

func parseMaxUnavailable(ms string, replicas int) int {
	if ms == "" {
		return 0
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
