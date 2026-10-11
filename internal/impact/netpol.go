package impact

import (
	"fmt"
	"strings"

	"github.com/justrunme/architecture-rehearsal/internal/graph"
)

// NetworkPolicyCut detects NetworkPolicy changes that would cut a known DEPENDS_ON edge.
type NetworkPolicyCut struct{}

func (NetworkPolicyCut) Name() string { return "networkpolicy-cut" }

func (d NetworkPolicyCut) Evaluate(ctx Context) Verdict {
	v := Verdict{Detector: d.Name(), Confidence: "medium"}
	if ctx.BaseIdx == nil || ctx.PropIdx == nil || ctx.Change == nil {
		v.Status = Unknown
		v.Explanation = "missing change or graph indexes"
		v.Missing = []string{"change", "graph"}
		v.Confidence = "low"
		return v
	}

	netpolTouched := false
	for _, n := range ctx.Change.AddedNodes {
		if n.Kind == graph.KindNetworkPolicy {
			netpolTouched = true
		}
	}
	for _, n := range ctx.Change.PatchNodes {
		if strings.HasPrefix(n.ID, "netpol/") {
			netpolTouched = true
		}
	}
	for _, id := range ctx.Change.Seeds {
		if strings.HasPrefix(id, "netpol/") {
			netpolTouched = true
		}
	}
	// Also detect when proposed graph gained NetworkPolicies vs baseline.
	if !netpolTouched {
		baseN := len(ctx.BaseIdx.ByKind[graph.KindNetworkPolicy])
		propN := len(ctx.PropIdx.ByKind[graph.KindNetworkPolicy])
		if propN > baseN {
			netpolTouched = true
		}
	}

	if !netpolTouched {
		v.Status = Pass
		v.Explanation = "no NetworkPolicy changes in the proposed sync"
		v.Confidence = "high"
		return v
	}

	deps := []graph.Edge{}
	for _, e := range ctx.Proposed.Edges {
		if e.Rel == graph.RelDependsOn {
			deps = append(deps, e)
		}
	}
	if len(deps) == 0 && ctx.Baseline != nil {
		for _, e := range ctx.Baseline.Edges {
			if e.Rel == graph.RelDependsOn {
				deps = append(deps, e)
			}
		}
	}
	if len(deps) == 0 {
		v.Status = Unknown
		v.Explanation = "NetworkPolicy changed but graph has no DEPENDS_ON edges to evaluate cut risk"
		v.Missing = []string{"DEPENDS_ON"}
		v.Confidence = "low"
		return v
	}

	policies := ctx.PropIdx.ByKind[graph.KindNetworkPolicy]
	if len(policies) == 0 {
		v.Status = Unknown
		v.Explanation = "NetworkPolicy change referenced but no NetworkPolicy nodes in proposed graph"
		v.Missing = []string{"NetworkPolicy"}
		v.Confidence = "low"
		return v
	}

	var cuts []string
	var affected []string
	for _, e := range deps {
		from := ctx.PropIdx.ByID[e.From]
		to := ctx.PropIdx.ByID[e.To]
		if from == nil {
			from = ctx.BaseIdx.ByID[e.From]
		}
		if to == nil {
			to = ctx.BaseIdx.ByID[e.To]
		}
		if from == nil || to == nil {
			continue
		}
		for _, np := range policies {
			if cutsEdge(np, from, to) {
				cuts = append(cuts, fmt.Sprintf(
					"NetworkPolicy %s/%s would cut dependency %s → %s",
					np.Namespace, np.Name, display(ctx.PropIdx, from.ID), display(ctx.PropIdx, to.ID)))
				affected = append(affected, from.ID, to.ID, np.ID)
			}
		}
	}

	if len(cuts) > 0 {
		v.Status = Block
		v.Explanation = "NetworkPolicy change cuts a known dependency edge (e.g. app → database)"
		v.Evidence = cuts
		v.Affected = unique(affected)
		v.Confidence = "high"
		return v
	}

	v.Status = Pass
	v.Explanation = "NetworkPolicy changes do not cut known DEPENDS_ON edges"
	v.Confidence = "medium"
	return v
}

// cutsEdge is a conservative heuristic: deny-all egress on the source, deny-all
// ingress on the destination, or selector-targeted policies with empty rules.
func cutsEdge(np, from, to *graph.Node) bool {
	if np == nil || from == nil || to == nil {
		return false
	}
	// Same namespace policies typically apply; cross-ns still evaluated when selectors match.
	sel, _ := np.Attributes["podSelector"].(map[string]any)
	fromLabels, _ := from.Attributes["podLabels"].(map[string]any)
	toLabels, _ := to.Attributes["podLabels"].(map[string]any)

	selectsFrom := selectorMatches(sel, fromLabels) || len(sel) == 0
	selectsTo := selectorMatches(sel, toLabels) || len(sel) == 0

	egressEmpty := np.AttrBool("egressEmpty")
	ingressEmpty := np.AttrBool("ingressEmpty")

	if selectsFrom && egressEmpty && (np.Namespace == "" || np.Namespace == from.Namespace) {
		return true
	}
	if selectsTo && ingressEmpty && (np.Namespace == "" || np.Namespace == to.Namespace) {
		// Deny-all ingress on dependency target blocks callers.
		return true
	}

	// Egress rules that only allow unrelated peers.
	if selectsFrom {
		if eg, ok := np.Attributes["egress"].([]any); ok && len(eg) > 0 {
			if !egressAllows(eg, to) {
				return true
			}
		}
	}
	return false
}

func selectorMatches(sel, labels map[string]any) bool {
	if len(sel) == 0 {
		return true // empty podSelector matches all pods in namespace
	}
	if labels == nil {
		return false
	}
	for k, v := range sel {
		if fmt.Sprint(labels[k]) != fmt.Sprint(v) {
			return false
		}
	}
	return true
}

func egressAllows(rules []any, to *graph.Node) bool {
	toLabels, _ := to.Attributes["podLabels"].(map[string]any)
	for _, r := range rules {
		rm, _ := r.(map[string]any)
		if rm == nil {
			continue
		}
		peers, _ := rm["to"].([]any)
		if len(peers) == 0 {
			// empty to = allow all
			return true
		}
		for _, p := range peers {
			pm, _ := p.(map[string]any)
			if pm == nil {
				continue
			}
			if ps, ok := pm["podSelector"].(map[string]any); ok {
				ml, _ := ps["matchLabels"].(map[string]any)
				if selectorMatches(ml, toLabels) {
					return true
				}
			}
			// Namespace selector alone is insufficient without proof — treat as not allowing specific peer.
		}
	}
	return false
}
