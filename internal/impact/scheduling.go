package impact

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/justrunme/architecture-rehearsal/internal/capacity"
	"github.com/justrunme/architecture-rehearsal/internal/graph"
	"github.com/justrunme/architecture-rehearsal/internal/podtemplate"
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

	// No Nodes in baseline: only fail closed when this sync changes scheduling-relevant fields.
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

	// Cluster-wide pods unread: never PASS a capacity check on incomplete occupancy.
	if changeNeedsCapacity(ctx) && clusterPodsUnreadable(ctx) {
		v.Status = Unknown
		v.Explanation = "replica or request increase needs cluster-wide pod inventory for capacity, but pods could not be listed"
		v.Missing = []string{"pods"}
		v.Evidence = []string{podsAbsentNote(ctx), fmt.Sprintf("replica_delta=%d surge=%d requested=%d", delta, surge, requested)}
		v.Confidence = "low"
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

	needCPU, needMem := addedResourceDemand(ctx)
	needsResources := needCPU > 0 || needMem > 0

	if requested == 0 && !needsResources {
		v.Status = Pass
		v.Explanation = "no additional replica, surge, or resource demand vs baseline"
		v.Confidence = "high"
		return v
	}

	if requested > 0 && requested > capRes.Available {
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

	// CPU/memory: missing pod requests count as 0 (scheduler semantics). Subtract
	// demand for new/surge pods (and request increases) from free allocatable.
	if needsResources {
		if needCPU > 0 && !capRes.HaveCPU {
			v.Status = Unknown
			v.Explanation = "replica or request increase needs node allocatable CPU, but it is unreadable"
			v.Missing = []string{"node.allocatable.cpu"}
			v.Evidence = []string{capRes.Detail, fmt.Sprintf("needCPUMillis=%d", needCPU)}
			v.Confidence = "low"
			return v
		}
		if needMem > 0 && !capRes.HaveMemory {
			v.Status = Unknown
			v.Explanation = "replica or request increase needs node allocatable memory, but it is unreadable"
			v.Missing = []string{"node.allocatable.memory"}
			v.Evidence = []string{capRes.Detail, fmt.Sprintf("needMemoryBytes=%d", needMem)}
			v.Confidence = "low"
			return v
		}
		if needCPU > 0 && needCPU > capRes.AvailableCPUMillis {
			v.Status = Block
			v.Explanation = fmt.Sprintf(
				"unschedulable: need %d CPU millicores for new/surge pods but only %d available",
				needCPU, capRes.AvailableCPUMillis)
			v.Evidence = []string{
				fmt.Sprintf("needCPUMillis=%d availableCPUMillis=%d", needCPU, capRes.AvailableCPUMillis),
				fmt.Sprintf("replica_delta=%d surge=%d", delta, surge),
				capRes.Detail,
			}
			v.Affected = ctx.Change.Seeds
			v.Confidence = "high"
			return v
		}
		if needMem > 0 && needMem > capRes.AvailableMemoryBytes {
			v.Status = Block
			v.Explanation = fmt.Sprintf(
				"unschedulable: need %d memory bytes for new/surge pods but only %d available",
				needMem, capRes.AvailableMemoryBytes)
			v.Evidence = []string{
				fmt.Sprintf("needMemoryBytes=%d availableMemoryBytes=%d", needMem, capRes.AvailableMemoryBytes),
				fmt.Sprintf("replica_delta=%d surge=%d", delta, surge),
				capRes.Detail,
			}
			v.Affected = ctx.Change.Seeds
			v.Confidence = "high"
			return v
		}
	}

	v.Status = Pass
	if requested > 0 {
		v.Explanation = fmt.Sprintf("scheduling capacity sufficient (%d available for %d requested)", capRes.Available, requested)
	} else {
		v.Explanation = "scheduling capacity sufficient for resource request increase"
	}
	v.Evidence = []string{capRes.Detail}
	if needCPU > 0 {
		v.Evidence = append(v.Evidence, fmt.Sprintf("needCPUMillis=%d availableCPUMillis=%d", needCPU, capRes.AvailableCPUMillis))
	}
	if needMem > 0 {
		v.Evidence = append(v.Evidence, fmt.Sprintf("needMemoryBytes=%d availableMemoryBytes=%d", needMem, capRes.AvailableMemoryBytes))
	}
	v.Confidence = "high"
	return v
}

// addedResourceDemand returns CPU/memory millicores/bytes required for new
// replica+surge pods plus any per-replica request increase on unchanged counts.
func addedResourceDemand(ctx Context) (cpuMillis, memBytes int64) {
	if ctx.PropIdx == nil {
		return 0, 0
	}
	for _, w := range ctx.PropIdx.ByKind[graph.KindWorkload] {
		baseR := 0
		var base *graph.Node
		if ctx.BaseIdx != nil {
			base = ctx.BaseIdx.ByID[w.ID]
			if base != nil {
				baseR = base.WorkloadReplicas()
			}
		}
		rep := w.WorkloadReplicas()
		pcpu, pmem := workloadPodRequests(w)
		bcpu, bmem := 0, 0
		if base != nil {
			bcpu, bmem = workloadPodRequests(base)
		}
		extraPods := 0
		if rep > baseR {
			extraPods = (rep - baseR) + parseMaxSurge(w.AttrString("maxSurge"), rep)
		}
		if extraPods > 0 {
			cpuMillis += int64(pcpu) * int64(extraPods)
			memBytes += int64(pmem) * int64(extraPods)
		}
		// Same replica count (or remaining replicas) with higher requests.
		stable := rep
		if extraPods > 0 {
			// Existing pods keep running during scale-up; their request delta also matters.
			stable = baseR
		}
		if stable > 0 {
			if pcpu > bcpu {
				cpuMillis += int64(pcpu-bcpu) * int64(stable)
			}
			if pmem > bmem {
				memBytes += int64(pmem-bmem) * int64(stable)
			}
		}
	}
	return cpuMillis, memBytes
}

func workloadPodRequests(w *graph.Node) (cpuMillis, memBytes int) {
	if w == nil || w.Attributes == nil {
		return 0, 0
	}
	if v := w.AttrInt("cpuRequestMillis"); v > 0 {
		cpuMillis = v
	}
	if v := w.AttrInt("memoryRequestBytes"); v > 0 {
		memBytes = v
	}
	spec := podTemplateSpec(w.Attributes)
	if spec == nil {
		return cpuMillis, memBytes
	}
	c, m := sumTemplateRequests(spec)
	if c > cpuMillis {
		cpuMillis = c
	}
	if m > memBytes {
		memBytes = m
	}
	return cpuMillis, memBytes
}

func sumTemplateRequests(spec map[string]any) (cpuMillis, memBytes int) {
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
			req, _ := res["requests"].(map[string]any)
			if req == nil {
				continue
			}
			cpuMillis += parseCPUMillisAttr(req["cpu"])
			memBytes += parseMemoryBytesAttr(req["memory"])
		}
	}
	return cpuMillis, memBytes
}

func parseCPUMillisAttr(v any) int {
	if v == nil {
		return 0
	}
	s := strings.TrimSpace(fmt.Sprint(v))
	if s == "" {
		return 0
	}
	if strings.HasSuffix(s, "m") {
		n, _ := strconv.Atoi(strings.TrimSuffix(s, "m"))
		return n
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return int(f * 1000)
}

func parseMemoryBytesAttr(v any) int {
	if v == nil {
		return 0
	}
	s := strings.TrimSpace(fmt.Sprint(v))
	if s == "" {
		return 0
	}
	mult := 1
	for _, suf := range []struct {
		s string
		m int
	}{
		{"Ki", 1024}, {"Mi", 1024 * 1024}, {"Gi", 1024 * 1024 * 1024},
		{"K", 1000}, {"M", 1000 * 1000}, {"G", 1000 * 1000 * 1000},
		{"k", 1000},
	} {
		if strings.HasSuffix(s, suf.s) {
			s = strings.TrimSuffix(s, suf.s)
			mult = suf.m
			break
		}
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return int(f * float64(mult))
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

// changeTouchesScheduling reports whether this sync CHANGES fields that need
// Node inventory: nodeSelector, affinity, tolerations, topologySpreadConstraints,
// container resource requests, or replica counts — compared desired vs live.
// Presence alone (e.g. unchanged affinity on an env-only patch) does not count.
func changeTouchesScheduling(ctx Context) bool {
	return changeNeedsCapacity(ctx) || schedulingFieldsDiffer(ctx)
}

// changeNeedsCapacity is true when the sync needs new pod slots or higher requests.
func changeNeedsCapacity(ctx Context) bool {
	if ctx.Change == nil {
		return false
	}
	for _, n := range ctx.Change.AddedNodes {
		if n.Kind == graph.KindWorkload || strings.HasPrefix(n.ID, "workload/") {
			return true
		}
	}
	for _, n := range ctx.Change.PatchNodes {
		baseR := 0
		var base *graph.Node
		if ctx.BaseIdx != nil {
			base = ctx.BaseIdx.ByID[n.ID]
			if base != nil {
				baseR = base.WorkloadReplicas()
			}
		}
		if propReplicas(n.Attributes, baseR) > baseR {
			return true
		}
		if requestsIncreased(n.Attributes, base) {
			return true
		}
	}
	if ctx.BaseIdx != nil && ctx.PropIdx != nil {
		if graph.TotalWorkloadReplicas(ctx.PropIdx) > graph.TotalWorkloadReplicas(ctx.BaseIdx) {
			return true
		}
	}
	return false
}

func schedulingFieldsDiffer(ctx Context) bool {
	if ctx.Change == nil {
		return false
	}
	for _, n := range ctx.Change.AddedNodes {
		if n.Kind == graph.KindWorkload || strings.HasPrefix(n.ID, "workload/") {
			fp := schedulingFingerprint(n.Attributes)
			if len(fp) > 0 {
				return true
			}
		}
	}
	for _, n := range ctx.Change.PatchNodes {
		var baseAttrs map[string]any
		if ctx.BaseIdx != nil {
			if bn := ctx.BaseIdx.ByID[n.ID]; bn != nil {
				baseAttrs = bn.Attributes
			}
		}
		desired := schedulingFingerprint(n.Attributes)
		live := schedulingFingerprint(baseAttrs)
		// Only fields present in desired are compared (subset); live-only defaults ignored.
		if schedulingFingerprintChanged(desired, live) {
			return true
		}
	}
	return false
}

func propReplicas(attrs map[string]any, fallback int) int {
	if attrs == nil {
		return fallback
	}
	switch t := attrs["replicas"].(type) {
	case int:
		return t
	case float64:
		return int(t)
	case string:
		v, err := strconv.Atoi(t)
		if err == nil {
			return v
		}
	}
	return fallback
}

func schedulingFingerprint(attrs map[string]any) map[string]any {
	if attrs == nil {
		return nil
	}
	fp := map[string]any{}
	spec := podTemplateSpec(attrs)
	copySched := func(key string) {
		if v, ok := attrs[key]; ok {
			fp[key] = v
			return
		}
		if spec != nil {
			if v, ok := spec[key]; ok {
				fp[key] = v
			}
		}
	}
	for _, key := range []string{"nodeSelector", "affinity", "tolerations", "topologySpreadConstraints"} {
		copySched(key)
	}
	if reqs := containerRequestsFingerprint(attrs, spec); len(reqs) > 0 {
		fp["requests"] = reqs
	}
	if len(fp) == 0 {
		return nil
	}
	return fp
}

func podTemplateSpec(attrs map[string]any) map[string]any {
	tpl, _ := attrs["podTemplate"].(map[string]any)
	if tpl == nil {
		return nil
	}
	spec, _ := tpl["spec"].(map[string]any)
	return spec
}

func containerRequestsFingerprint(attrs map[string]any, spec map[string]any) map[string]any {
	out := map[string]any{}
	if spec == nil {
		spec = podTemplateSpec(attrs)
	}
	if spec == nil {
		return out
	}
	for _, key := range []string{"containers", "initContainers"} {
		list, _ := spec[key].([]any)
		for _, c := range list {
			cm, _ := c.(map[string]any)
			if cm == nil {
				continue
			}
			name := fmt.Sprint(cm["name"])
			res, _ := cm["resources"].(map[string]any)
			if res == nil {
				continue
			}
			req, _ := res["requests"].(map[string]any)
			if len(req) == 0 {
				continue
			}
			out[key+"/"+name] = req
		}
	}
	return out
}

func schedulingFingerprintChanged(desired, live map[string]any) bool {
	if len(desired) == 0 {
		return false
	}
	if live == nil {
		live = map[string]any{}
	}
	for k, dv := range desired {
		lv, ok := live[k]
		if !ok {
			return true
		}
		dm, dOK := dv.(map[string]any)
		lm, lOK := lv.(map[string]any)
		if dOK && lOK {
			if !podtemplate.DeepEqual(dm, lm) {
				return true
			}
			continue
		}
		// Lists (tolerations, topologySpread) — compare via wrapped maps.
		if !podtemplate.DeepEqual(map[string]any{"v": dv}, map[string]any{"v": lv}) {
			return true
		}
	}
	return false
}

func requestsIncreased(desiredAttrs map[string]any, base *graph.Node) bool {
	var baseAttrs map[string]any
	if base != nil {
		baseAttrs = base.Attributes
	}
	dReq := containerRequestsFingerprint(desiredAttrs, nil)
	if len(dReq) == 0 {
		return false
	}
	lReq := containerRequestsFingerprint(baseAttrs, nil)
	for k, dv := range dReq {
		dm, _ := dv.(map[string]any)
		lm, _ := lReq[k].(map[string]any)
		if lm == nil {
			return true
		}
		if requestMapIncreased(dm, lm) {
			return true
		}
	}
	return false
}

func requestMapIncreased(desired, live map[string]any) bool {
	for k, dv := range desired {
		if quantityGreater(dv, live[k]) {
			return true
		}
	}
	return false
}

func quantityGreater(desired, live any) bool {
	if live == nil {
		return desired != nil && fmt.Sprint(desired) != "" && fmt.Sprint(desired) != "0"
	}
	// Compare as strings of resource quantities when possible; fall back to Sprint.
	ds, ls := fmt.Sprint(desired), fmt.Sprint(live)
	if ds == ls {
		return false
	}
	di, dOK := parseRoughQuantity(ds)
	li, lOK := parseRoughQuantity(ls)
	if dOK && lOK {
		return di > li
	}
	return ds != ls // treat any unresolved change as needing capacity check
}

func parseRoughQuantity(s string) (int64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, true
	}
	// CPU millicores
	if strings.HasSuffix(s, "m") {
		n, err := strconv.ParseInt(strings.TrimSuffix(s, "m"), 10, 64)
		return n, err == nil
	}
	mult := int64(1)
	isCPUCores := true
	for _, suf := range []struct {
		s string
		m int64
	}{
		{"Ki", 1024}, {"Mi", 1024 * 1024}, {"Gi", 1024 * 1024 * 1024},
		{"K", 1000}, {"M", 1000 * 1000}, {"G", 1000 * 1000 * 1000},
		{"k", 1000},
	} {
		if strings.HasSuffix(s, suf.s) {
			s = strings.TrimSuffix(s, suf.s)
			mult = suf.m
			isCPUCores = false
			break
		}
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false
	}
	if isCPUCores {
		return int64(f * 1000), true
	}
	return int64(f * float64(mult)), true
}

func clusterPodsUnreadable(ctx Context) bool {
	if ctx.Baseline == nil || ctx.Baseline.Meta == nil {
		return false
	}
	if err, ok := ctx.Baseline.Meta["pods_collect_error"].(string); ok && err != "" {
		return true
	}
	if ok, exists := ctx.Baseline.Meta["pods_collect_ok"].(bool); exists && !ok {
		return true
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

func podsAbsentNote(ctx Context) string {
	if ctx.Baseline != nil && ctx.Baseline.Meta != nil {
		if err, ok := ctx.Baseline.Meta["pods_collect_error"].(string); ok && err != "" {
			return "pods collect failed: " + err
		}
		if ok, _ := ctx.Baseline.Meta["pods_collect_ok"].(bool); ok == false {
			return "pods collect skipped or failed"
		}
	}
	return "cluster-wide pod inventory unavailable"
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
