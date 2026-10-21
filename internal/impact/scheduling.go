package impact

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/justrunme/architecture-rehearsal/internal/capacity"
	"github.com/justrunme/architecture-rehearsal/internal/graph"
)

// SchedulingCapacity checks new replicas vs node allocatable, topology spread,
// affinity/anti-affinity, and taints/tolerations.
type SchedulingCapacity struct{}

func (SchedulingCapacity) Name() string { return "scheduling-capacity" }

func (d SchedulingCapacity) Evaluate(ctx Context) Verdict {
	v := Verdict{Detector: d.Name(), Confidence: "medium"}
	if ctx.Change == nil || ctx.BaseIdx == nil || ctx.PropIdx == nil {
		v.Status = Unknown
		v.Explanation = "missing change or graph indexes"
		v.Missing = []string{"change", "graph"}
		v.Confidence = "low"
		return v
	}
	if !isArgoChange(ctx.Change) && ctx.Change.EffectiveKind() != "scale-up" {
		v.Status = Pass
		v.Explanation = "change kind not applicable to scheduling capacity"
		v.Confidence = "high"
		return v
	}

	baseRep := graph.TotalWorkloadReplicas(ctx.BaseIdx)
	propRep := graph.TotalWorkloadReplicas(ctx.PropIdx)
	delta := propRep - baseRep
	if delta < 0 {
		delta = 0
	}
	surge := 0
	for _, n := range ctx.PropIdx.ByKind[graph.KindWorkload] {
		bn := ctx.BaseIdx.ByID[n.ID]
		baseR := 0
		if bn != nil {
			baseR = bn.WorkloadReplicas()
		}
		rep := n.WorkloadReplicas()
		if rep > baseR {
			surge += parseMaxSurge(n.AttrString("maxSurge"), rep)
		}
	}
	requested := delta + surge

	prov := capacity.SchedulingEstimate{}
	capRes, capErr := prov.Read(ctx.Baseline)
	nodes := ctx.BaseIdx.ByKind[graph.KindNode]
	hasTaints := false
	hasZone := false
	for _, n := range nodes {
		if n.AttrString("zone") != "" {
			hasZone = true
		}
		if _, ok := n.Attributes["taints"]; ok {
			hasTaints = true
		}
	}

	// No Nodes in baseline: only fail closed when this sync touches scheduling-relevant fields.
	if len(nodes) == 0 {
		if changeTouchesScheduling(ctx) {
			v.Status = Unknown
			v.Explanation = "scheduling-relevant change but no Node objects in baseline"
			v.Missing = []string{"nodes"}
			v.Evidence = []string{nodesAbsentNote(ctx)}
			v.Confidence = "low"
			return v
		}
		v.Status = Pass
		v.Explanation = "no Node objects in baseline; change does not touch scheduling-relevant fields"
		v.Evidence = []string{nodesAbsentNote(ctx)}
		v.Confidence = "medium"
		return v
	}

	// Topology / affinity checks on proposed workloads.
	topoIssues := []string{}
	affinityIssues := []string{}
	taintIssues := []string{}
	for _, w := range ctx.PropIdx.ByKind[graph.KindWorkload] {
		if w.AttrBool("hasTopologySpread") {
			if !hasZone {
				topoIssues = append(topoIssues, fmt.Sprintf("%s: topology spread set but nodes lack zone labels", display(ctx.PropIdx, w.ID)))
			}
		}
		if w.AttrBool("hasAffinity") {
			if anti := hardAntiAffinity(w); anti {
				// Same-namespace workload anti-affinity with insufficient distinct nodes.
				if len(nodes) < w.WorkloadReplicas() {
					affinityIssues = append(affinityIssues,
						fmt.Sprintf("%s: required anti-affinity needs %d nodes, only %d available",
							display(ctx.PropIdx, w.ID), w.WorkloadReplicas(), len(nodes)))
				}
			}
		}
		if ns, ok := w.Attributes["nodeSelector"].(map[string]any); ok && len(ns) > 0 {
			if !nodesHaveLabelData(nodes) {
				v.Status = Unknown
				v.Explanation = "nodeSelector present but node label data is absent from the snapshot"
				v.Missing = []string{"node.labels"}
				v.Confidence = "low"
				return v
			}
			match := 0
			for _, n := range nodes {
				if nodeMatchesSelector(n, ns) {
					match++
				}
			}
			if match == 0 {
				affinityIssues = append(affinityIssues,
					fmt.Sprintf("%s: nodeSelector matches zero nodes", display(ctx.PropIdx, w.ID)))
			}
		}
		if hasTaints {
			// If nodes declare taints and workload has no tolerations for them.
			if !workloadTolerates(w, nodes) {
				taintIssues = append(taintIssues,
					fmt.Sprintf("%s: no toleration for node taints", display(ctx.PropIdx, w.ID)))
			}
		}
	}

	if requested > 0 && capErr != nil {
		v.Status = Unknown
		v.Explanation = "replica increase requires scheduling capacity data, but none is available"
		v.Missing = []string{"node.allocatablePods", "pod_scheduling_capacity_estimate"}
		v.Evidence = []string{fmt.Sprintf("replica_delta=%d surge=%d requested=%d", delta, surge, requested)}
		v.Confidence = "low"
		return v
	}

	if len(affinityIssues) > 0 {
		v.Status = Block
		v.Explanation = "affinity/nodeSelector would leave pods unschedulable"
		v.Evidence = affinityIssues
		v.Affected = componentIDs(ctx.PropIdx, affinityIssues)
		v.Confidence = "high"
		return v
	}
	if len(taintIssues) > 0 {
		v.Status = Block
		v.Explanation = "node taints without matching tolerations would block scheduling"
		v.Evidence = taintIssues
		v.Confidence = "high"
		return v
	}
	if len(topoIssues) > 0 {
		v.Status = Warn
		v.Explanation = "topologySpreadConstraints may be infeasible without zone-labeled nodes"
		v.Evidence = topoIssues
		v.Confidence = "medium"
		return v
	}

	if requested == 0 {
		v.Status = Pass
		v.Explanation = "no additional replica or surge demand vs baseline"
		v.Confidence = "high"
		return v
	}

	if requested > capRes.Available {
		v.Status = Block
		v.Explanation = fmt.Sprintf(
			"unschedulable: need %d pods (delta %d + surge %d) but allocatable capacity is %d (%s)",
			requested, delta, surge, capRes.Available, capRes.Source)
		v.Evidence = []string{
			fmt.Sprintf("baseline_replicas=%d", baseRep),
			fmt.Sprintf("proposed_replicas=%d", propRep),
			fmt.Sprintf("requested=%d available=%d", requested, capRes.Available),
			capRes.Detail,
		}
		v.Affected = ctx.Change.Seeds
		v.Confidence = "high"
		return v
	}

	v.Status = Pass
	v.Explanation = fmt.Sprintf("scheduling capacity sufficient (%d available for %d requested)", capRes.Available, requested)
	v.Evidence = []string{capRes.Detail}
	v.Confidence = "high"
	return v
}

// parseMaxSurge follows Kubernetes RollingUpdate semantics: percentage values
// round UP; the default when unset is 25%.
func parseMaxSurge(ms string, replicas int) int {
	if ms == "" {
		return int(math.Ceil(float64(replicas) * 0.25))
	}
	if strings.HasSuffix(ms, "%") {
		p, _ := strconv.Atoi(strings.TrimSuffix(ms, "%"))
		return int(math.Ceil(float64(replicas) * float64(p) / 100.0))
	}
	v, _ := strconv.Atoi(ms)
	if v < 0 {
		return 0
	}
	return v
}

// changeTouchesScheduling reports whether this sync changes fields that need
// Node inventory to evaluate: nodeSelector, affinity, tolerations,
// topologySpreadConstraints, container resource requests, or replica counts.
func changeTouchesScheduling(ctx Context) bool {
	if ctx.Change == nil {
		return false
	}
	check := func(id string, attrs map[string]any) bool {
		if attrs == nil {
			return false
		}
		if _, ok := attrs["nodeSelector"]; ok {
			return true
		}
		if _, ok := attrs["affinity"]; ok {
			return true
		}
		if attrs["hasAffinity"] == true {
			return true
		}
		if _, ok := attrs["tolerations"]; ok {
			return true
		}
		if attrs["hasTopologySpread"] == true {
			return true
		}
		if _, ok := attrs["topologySpreadConstraints"]; ok {
			return true
		}
		if tpl, ok := attrs["podTemplate"].(map[string]any); ok {
			if podTemplateTouchesScheduling(tpl) {
				return true
			}
		}
		if _, ok := attrs["replicas"]; ok {
			baseR := 0
			if ctx.BaseIdx != nil {
				if bn := ctx.BaseIdx.ByID[id]; bn != nil {
					baseR = bn.WorkloadReplicas()
				}
			}
			propR := 0
			switch t := attrs["replicas"].(type) {
			case int:
				propR = t
			case float64:
				propR = int(t)
			default:
				propR = baseR
			}
			if propR != baseR {
				return true
			}
		}
		return false
	}
	for _, n := range ctx.Change.AddedNodes {
		if check(n.ID, n.Attributes) {
			return true
		}
		// New workload always implies replica demand.
		if n.Kind == graph.KindWorkload || strings.HasPrefix(n.ID, "workload/") {
			return true
		}
	}
	for _, n := range ctx.Change.PatchNodes {
		if check(n.ID, n.Attributes) {
			return true
		}
	}
	return false
}

func podTemplateTouchesScheduling(tpl map[string]any) bool {
	spec, _ := tpl["spec"].(map[string]any)
	if spec == nil {
		return false
	}
	if _, ok := spec["nodeSelector"]; ok {
		return true
	}
	if _, ok := spec["affinity"]; ok {
		return true
	}
	if _, ok := spec["tolerations"]; ok {
		return true
	}
	if _, ok := spec["topologySpreadConstraints"]; ok {
		return true
	}
	for _, key := range []string{"containers", "initContainers"} {
		list, _ := spec[key].([]any)
		for _, c := range list {
			cm, _ := c.(map[string]any)
			if cm == nil {
				continue
			}
			res, _ := cm["resources"].(map[string]any)
			if res == nil {
				continue
			}
			if req, ok := res["requests"].(map[string]any); ok && len(req) > 0 {
				return true
			}
		}
	}
	return false
}

func nodesAbsentNote(ctx Context) string {
	if ctx.Baseline != nil && ctx.Baseline.Meta != nil {
		if err, ok := ctx.Baseline.Meta["nodes_collect_error"].(string); ok && err != "" {
			return "nodes collect failed: " + err
		}
		if ok, _ := ctx.Baseline.Meta["nodes_collect_ok"].(bool); ok == false {
			return "nodes collect skipped or failed"
		}
	}
	return "baseline has no Node objects"
}

func hardAntiAffinity(w *graph.Node) bool {
	aff, ok := w.Attributes["affinity"].(map[string]any)
	if !ok {
		return false
	}
	pod, ok := aff["podAntiAffinity"].(map[string]any)
	if !ok {
		return false
	}
	_, has := pod["requiredDuringSchedulingIgnoredDuringExecution"]
	return has
}

// nodesHaveLabelData reports whether the snapshot captured node metadata.labels.
// Zone-only attributes are not enough: selectors like kubernetes.io/os must not
// be treated as "matches zero nodes" when labels were never collected.
func nodesHaveLabelData(nodes []*graph.Node) bool {
	for _, n := range nodes {
		if n == nil || n.Attributes == nil {
			continue
		}
		if _, ok := n.Attributes["labels"]; ok {
			return true
		}
	}
	return false
}

func nodeMatchesSelector(n *graph.Node, sel map[string]any) bool {
	labels, _ := n.Attributes["labels"].(map[string]any)
	if labels == nil {
		labels = map[string]any{}
	}
	// Merge flat zone attribute when present (collector still stores it).
	if _, ok := labels["topology.kubernetes.io/zone"]; !ok {
		if z := n.AttrString("zone"); z != "" {
			labels["topology.kubernetes.io/zone"] = z
		}
	}
	for k, v := range sel {
		if fmt.Sprint(labels[k]) != fmt.Sprint(v) {
			return false
		}
	}
	return true
}

func workloadTolerates(w *graph.Node, nodes []*graph.Node) bool {
	tols, _ := w.Attributes["tolerations"].([]any)
	for _, n := range nodes {
		raw, ok := n.Attributes["taints"].([]any)
		if !ok || len(raw) == 0 {
			continue
		}
		for _, t := range raw {
			tm, _ := t.(map[string]any)
			if tm == nil {
				continue
			}
			key := fmt.Sprint(tm["key"])
			effect := fmt.Sprint(tm["effect"])
			if effect == "" {
				effect = "NoSchedule"
			}
			if effect != "NoSchedule" && effect != "NoExecute" {
				continue
			}
			okTol := false
			for _, tol := range tols {
				tolm, _ := tol.(map[string]any)
				if tolm == nil {
					continue
				}
				if fmt.Sprint(tolm["key"]) == key {
					okTol = true
					break
				}
			}
			if !okTol {
				return false
			}
		}
	}
	return true
}

func display(idx *graph.Index, id string) string {
	if n := idx.ByID[id]; n != nil {
		if n.Namespace != "" {
			return n.Namespace + "/" + n.Name
		}
		return n.Name
	}
	return id
}

func componentIDs(idx *graph.Index, msgs []string) []string {
	var out []string
	for _, id := range idx.ByKind[graph.KindWorkload] {
		for _, m := range msgs {
			if strings.Contains(m, id.Name) || strings.Contains(m, id.ID) {
				out = append(out, id.ID)
			}
		}
	}
	return out
}
