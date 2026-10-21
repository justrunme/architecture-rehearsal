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

	delta, surge := 0, 0
	for _, n := range ctx.PropIdx.ByKind[graph.KindWorkload] {
		d, s := workloadPodDemand(ctx, n)
		delta += d
		surge += s
	}
	requested := delta + surge

	prov := capacity.SchedulingEstimate{}
	capRes, capErr := prov.Read(ctx.Baseline)
	nodes := ctx.BaseIdx.ByKind[graph.KindNode]
	hasZone := false
	for _, n := range nodes {
		if n.AttrString("zone") != "" {
			hasZone = true
		}
	}

	needsCap := changeNeedsCapacity(ctx)

	// No Nodes in baseline: fail closed when slots/requests are needed OR scheduling fields change.
	if len(nodes) == 0 {
		if needsCap || changeTouchesScheduling(ctx) {
			v.Status = Unknown
			v.Explanation = "scheduling capacity or scheduling-relevant change needs Node objects, but none are in baseline"
			v.Missing = []string{"nodes"}
			v.Evidence = []string{nodesAbsentNote(ctx), fmt.Sprintf("replica_delta=%d surge=%d requested=%d", delta, surge, requested)}
			v.Confidence = "low"
			return v
		}
		v.Status = Pass
		v.Explanation = "no Node objects in baseline; change does not need capacity or touch scheduling-relevant fields"
		v.Evidence = []string{nodesAbsentNote(ctx)}
		v.Confidence = "medium"
		return v
	}

	// Cluster-wide pods unread: never PASS a capacity check on incomplete occupancy.
	if needsCap && clusterPodsUnreadable(ctx) {
		v.Status = Unknown
		v.Explanation = "replica, surge, or request increase needs cluster-wide pod inventory for capacity, but pods could not be listed"
		v.Missing = []string{"pods"}
		v.Evidence = []string{podsAbsentNote(ctx), fmt.Sprintf("replica_delta=%d surge=%d requested=%d", delta, surge, requested)}
		v.Confidence = "low"
		return v
	}

	// Topology / affinity checks on proposed workloads.
	topoIssues := []string{}
	affinityIssues := []string{}
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
		if sel := workloadNodeSelector(w); len(sel) > 0 {
			if !nodesHaveLabelData(nodes) {
				v.Status = Unknown
				v.Explanation = "nodeSelector present but node label data is absent from the snapshot"
				v.Missing = []string{"node.labels"}
				v.Confidence = "low"
				return v
			}
		}
		// Require ≥1 eligible node when this sync needs placement OR changes
		// selector/affinity/tolerations. Pure no-ops never BLOCK on capacity/taints
		// (a tainted control-plane must not fail a 3→3 with a roomy worker).
		if workloadNeedsPlacement(ctx, w) || workloadEligibilitySpecChanged(ctx, w) {
			elig := filterEligibleNodes(nodes, w)
			if len(elig) == 0 {
				affinityIssues = append(affinityIssues,
					fmt.Sprintf("%s: no eligible nodes (nodeSelector/affinity/taints/cordon)", display(ctx.PropIdx, w.ID)))
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
		v.Explanation = "no eligible nodes for new/surge pods (selector, affinity, taints, or cordon)"
		v.Evidence = affinityIssues
		v.Affected = componentIDs(ctx.PropIdx, affinityIssues)
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

	// Per-node fit for new/surge pods (and request increases on stable replicas).
	fit := evaluatePerNodeFit(ctx, nodes, capRes)
	if fit.Status != "" {
		v.Status = fit.Status
		v.Explanation = fit.Explanation
		v.Evidence = fit.Evidence
		v.Missing = fit.Missing
		v.Affected = ctx.Change.Seeds
		v.Confidence = fit.Confidence
		return v
	}

	v.Status = Pass
	if requested > 0 {
		v.Explanation = fmt.Sprintf("scheduling capacity sufficient (%d new pods: delta %d + surge %d)", requested, delta, surge)
	} else if needsCap {
		v.Explanation = "scheduling capacity sufficient for resource request increase"
	} else {
		v.Explanation = "scheduling capacity sufficient"
	}
	v.Evidence = []string{capRes.Detail}
	v.Evidence = append(v.Evidence, fit.Evidence...)
	v.Confidence = "high"
	return v
}

// workloadPodDemand returns replica delta and surge pods for one workload.
// Surge applies only when the pod template changes under RollingUpdate (not Recreate).
// Plain scale (unchanged template) counts only added replicas.
func workloadPodDemand(ctx Context, w *graph.Node) (delta, surge int) {
	if w == nil {
		return 0, 0
	}
	baseR := 0
	if ctx.BaseIdx != nil {
		if bn := ctx.BaseIdx.ByID[w.ID]; bn != nil {
			baseR = bn.WorkloadReplicas()
		}
	}
	rep := w.WorkloadReplicas()
	if rep > baseR {
		delta = rep - baseR
	}
	if isRecreateStrategy(w) {
		return delta, 0
	}
	if syncTriggersRollout(ctx, w) {
		surge = parseMaxSurge(w.AttrString("maxSurge"), rep)
	}
	return delta, surge
}

func isRecreateStrategy(w *graph.Node) bool {
	if w == nil {
		return false
	}
	return strings.EqualFold(w.AttrString("strategyType"), "Recreate")
}

type fitResult struct {
	Status      string
	Explanation string
	Evidence    []string
	Missing     []string
	Confidence  string
}

// evaluatePerNodeFit places new/surge pods onto eligible nodes using free
// allocatable − existing pod requests per node. Returns zero-value Status on PASS.
func evaluatePerNodeFit(ctx Context, nodes []*graph.Node, capRes capacity.Result) fitResult {
	demands := collectPodDemands(ctx)
	if len(demands) == 0 {
		return fitResult{}
	}

	needsCPU, needsMem := false, false
	for _, d := range demands {
		if d.CPU > 0 {
			needsCPU = true
		}
		if d.Mem > 0 {
			needsMem = true
		}
	}
	if needsCPU && !capRes.HaveCPU {
		return fitResult{
			Status: Unknown, Confidence: "low",
			Explanation: "replica or request increase needs node allocatable CPU, but it is unreadable",
			Missing:     []string{"node.allocatable.cpu"},
			Evidence:    []string{capRes.Detail},
		}
	}
	if needsMem && !capRes.HaveMemory {
		return fitResult{
			Status: Unknown, Confidence: "low",
			Explanation: "replica or request increase needs node allocatable memory, but it is unreadable",
			Missing:     []string{"node.allocatable.memory"},
			Evidence:    []string{capRes.Detail},
		}
	}

	free := buildNodeFree(ctx, nodes)
	if len(free) == 0 {
		return fitResult{
			Status: Unknown, Confidence: "low",
			Explanation: "no node capacity inventory available for placement",
			Missing:     []string{"nodes"},
		}
	}

	var evidence []string
	for i, d := range demands {
		placed := false
		for j := range free {
			n := &free[j]
			if d.Workload != nil && !nodeEligibleForWorkload(n.node, d.Workload) {
				continue
			}
			if n.pods < 1 {
				continue
			}
			if needsCPU && n.cpu < d.CPU {
				continue
			}
			if needsMem && n.mem < d.Mem {
				continue
			}
			n.pods--
			n.cpu -= d.CPU
			n.mem -= d.Mem
			placed = true
			break
		}
		if !placed {
			return fitResult{
				Status: Block, Confidence: "high",
				Explanation: fmt.Sprintf(
					"unschedulable: cannot place new/surge pod %d/%d (%d CPU millicores, %d memory bytes) on any eligible node",
					i+1, len(demands), d.CPU, d.Mem),
				Evidence: append(evidence, nodeFreeSummary(free), capRes.Detail),
			}
		}
	}
	evidence = append(evidence, fmt.Sprintf("placed %d new/surge pods on eligible nodes", len(demands)))
	return fitResult{Evidence: evidence}
}

type podDemand struct {
	CPU, Mem int64
	Workload *graph.Node
}

func collectPodDemands(ctx Context) []podDemand {
	var out []podDemand
	if ctx.PropIdx == nil {
		return out
	}
	for _, w := range ctx.PropIdx.ByKind[graph.KindWorkload] {
		delta, surge := workloadPodDemand(ctx, w)
		extra := delta + surge
		pcpu, pmem := workloadPodRequests(w)
		for i := 0; i < extra; i++ {
			out = append(out, podDemand{CPU: int64(pcpu), Mem: int64(pmem), Workload: w})
		}
		// Request increase on pods that keep running (stable replicas).
		baseR := 0
		var base *graph.Node
		if ctx.BaseIdx != nil {
			base = ctx.BaseIdx.ByID[w.ID]
			if base != nil {
				baseR = base.WorkloadReplicas()
			}
		}
		bcpu, bmem := 0, 0
		if base != nil {
			bcpu, bmem = workloadPodRequests(base)
		}
		stable := baseR
		if stable < 0 {
			stable = 0
		}
		dCPU, dMem := int64(0), int64(0)
		if pcpu > bcpu {
			dCPU = int64(pcpu - bcpu)
		}
		if pmem > bmem {
			dMem = int64(pmem - bmem)
		}
		if (dCPU > 0 || dMem > 0) && stable > 0 {
			// Model as needing to place the delta on nodes that already host
			// those replicas — conservatively require free capacity for the increase.
			for i := 0; i < stable; i++ {
				out = append(out, podDemand{CPU: dCPU, Mem: dMem, Workload: w})
			}
		}
	}
	return out
}

type nodeFree struct {
	node     *graph.Node
	pods     int64
	cpu, mem int64
}

func buildNodeFree(ctx Context, nodes []*graph.Node) []nodeFree {
	out := make([]nodeFree, 0, len(nodes))
	for _, n := range nodes {
		if n == nil {
			continue
		}
		nf := nodeFree{
			node: n,
			pods: int64(n.AttrInt("allocatablePods")),
			cpu:  int64(n.AttrInt("allocatableCPUMillis")),
			mem:  int64(n.AttrInt("allocatableMemoryBytes")),
		}
		out = append(out, nf)
	}
	if ctx.Baseline == nil {
		return out
	}
	for i := range ctx.Baseline.Nodes {
		p := &ctx.Baseline.Nodes[i]
		if p.Kind != graph.KindPod {
			continue
		}
		phase := p.AttrString("phase")
		if phase == "Succeeded" || phase == "Failed" {
			continue
		}
		nn := p.AttrString("nodeName")
		cpu := int64(p.AttrInt("cpuRequestMillis"))
		mem := int64(p.AttrInt("memoryRequestBytes"))
		for j := range out {
			if nn != "" && out[j].node.Name != nn && out[j].node.ID != "node/"+nn {
				continue
			}
			if nn == "" {
				// Unscheduled Pending: charge the fullest node (conservative).
				continue
			}
			out[j].pods--
			out[j].cpu -= cpu
			out[j].mem -= mem
			break
		}
		if nn == "" {
			// Spread Pending cost onto the node with most free CPU.
			best := -1
			for j := range out {
				if best < 0 || out[j].cpu > out[best].cpu {
					best = j
				}
			}
			if best >= 0 {
				out[best].pods--
				out[best].cpu -= cpu
				out[best].mem -= mem
			}
		}
	}
	return out
}

func nodeFreeSummary(free []nodeFree) string {
	parts := make([]string, 0, len(free))
	for _, n := range free {
		name := n.node.Name
		parts = append(parts, fmt.Sprintf("%s:pods=%d,cpu=%d,mem=%d", name, n.pods, n.cpu, n.mem))
	}
	return "nodeFree=[" + strings.Join(parts, "; ") + "]"
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
	c, m := effectivePodRequests(spec)
	if c > cpuMillis {
		cpuMillis = c
	}
	if m > memBytes {
		memBytes = m
	}
	return cpuMillis, memBytes
}

// effectivePodRequests matches kube-scheduler: max(max(init), sum(containers)) + overhead.
func effectivePodRequests(spec map[string]any) (cpuMillis, memBytes int) {
	var maxInitCPU, maxInitMem int
	if list, _ := spec["initContainers"].([]any); len(list) > 0 {
		for _, c := range list {
			cm, _ := c.(map[string]any)
			cpu, mem := containerRequests(cm)
			if cpu > maxInitCPU {
				maxInitCPU = cpu
			}
			if mem > maxInitMem {
				maxInitMem = mem
			}
		}
	}
	var sumCPU, sumMem int
	if list, _ := spec["containers"].([]any); len(list) > 0 {
		for _, c := range list {
			cm, _ := c.(map[string]any)
			cpu, mem := containerRequests(cm)
			sumCPU += cpu
			sumMem += mem
		}
	}
	cpuMillis = sumCPU
	if maxInitCPU > cpuMillis {
		cpuMillis = maxInitCPU
	}
	memBytes = sumMem
	if maxInitMem > memBytes {
		memBytes = maxInitMem
	}
	if oh, ok := spec["overhead"].(map[string]any); ok {
		cpuMillis += parseCPUMillisAttr(oh["cpu"])
		memBytes += parseMemoryBytesAttr(oh["memory"])
	}
	return cpuMillis, memBytes
}

func containerRequests(cm map[string]any) (cpu, mem int) {
	if cm == nil {
		return 0, 0
	}
	res, _ := cm["resources"].(map[string]any)
	if res == nil {
		return 0, 0
	}
	req, _ := res["requests"].(map[string]any)
	if req == nil {
		return 0, 0
	}
	return parseCPUMillisAttr(req["cpu"]), parseMemoryBytesAttr(req["memory"])
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
		{"Ei", 1024 * 1024 * 1024 * 1024 * 1024 * 1024},
		{"Pi", 1024 * 1024 * 1024 * 1024 * 1024},
		{"Ti", 1024 * 1024 * 1024 * 1024},
		{"Gi", 1024 * 1024 * 1024},
		{"Mi", 1024 * 1024},
		{"Ki", 1024},
		{"E", 1000 * 1000 * 1000 * 1000 * 1000 * 1000},
		{"P", 1000 * 1000 * 1000 * 1000 * 1000},
		{"T", 1000 * 1000 * 1000 * 1000},
		{"G", 1000 * 1000 * 1000},
		{"M", 1000 * 1000},
		{"K", 1000},
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

// changeTouchesScheduling reports whether this sync CHANGES scheduling fields
// that need Node inventory when Nodes are absent (selector/affinity/tolerations/
// topology/requests DIFF, replica increases, or new workloads).
func changeTouchesScheduling(ctx Context) bool {
	if schedulingFieldsDiffer(ctx) {
		return true
	}
	return replicaDeltaOnly(ctx) > 0 || hasNewWorkload(ctx)
}

// changeNeedsCapacity is true when the sync needs new pod slots (delta and/or
// surge from a template change) or higher requests. Used when nodes or pods
// cannot be read — never false-PASS/ALLOW a change that needs placement.
func changeNeedsCapacity(ctx Context) bool {
	if ctx.Change == nil {
		return false
	}
	if hasNewWorkload(ctx) {
		return true
	}
	if ctx.PropIdx != nil {
		for _, w := range ctx.PropIdx.ByKind[graph.KindWorkload] {
			d, s := workloadPodDemand(ctx, w)
			if d+s > 0 {
				return true
			}
			var base *graph.Node
			if ctx.BaseIdx != nil {
				base = ctx.BaseIdx.ByID[w.ID]
			}
			if requestsIncreased(w.Attributes, base) {
				return true
			}
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
	return false
}

func hasNewWorkload(ctx Context) bool {
	if ctx.Change == nil {
		return false
	}
	for _, n := range ctx.Change.AddedNodes {
		if n.Kind == graph.KindWorkload || strings.HasPrefix(n.ID, "workload/") {
			return true
		}
	}
	return false
}

func replicaDeltaOnly(ctx Context) int {
	n := 0
	if ctx.PropIdx == nil {
		return 0
	}
	for _, w := range ctx.PropIdx.ByKind[graph.KindWorkload] {
		d, _ := workloadPodDemand(ctx, w)
		n += d
	}
	return n
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
	labels := nodeLabels(n)
	for k, v := range sel {
		if fmt.Sprint(labels[k]) != fmt.Sprint(v) {
			return false
		}
	}
	return true
}

func nodeLabels(n *graph.Node) map[string]any {
	if n == nil || n.Attributes == nil {
		return map[string]any{}
	}
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
	return labels
}

func workloadNeedsPlacement(ctx Context, w *graph.Node) bool {
	if w == nil {
		return false
	}
	d, s := workloadPodDemand(ctx, w)
	if d+s > 0 {
		return true
	}
	var base *graph.Node
	if ctx.BaseIdx != nil {
		base = ctx.BaseIdx.ByID[w.ID]
	}
	return requestsIncreased(w.Attributes, base)
}

func workloadEligibilitySpecChanged(ctx Context, w *graph.Node) bool {
	if w == nil {
		return false
	}
	var baseAttrs map[string]any
	if ctx.BaseIdx != nil {
		if bn := ctx.BaseIdx.ByID[w.ID]; bn != nil {
			baseAttrs = bn.Attributes
		}
	}
	desired := map[string]any{}
	live := map[string]any{}
	for _, key := range []string{"nodeSelector", "affinity", "tolerations"} {
		if v := schedulingFingerprintField(w.Attributes, key); v != nil {
			desired[key] = v
		}
		if v := schedulingFingerprintField(baseAttrs, key); v != nil {
			live[key] = v
		}
	}
	return schedulingFingerprintChanged(desired, live)
}

func schedulingFingerprintField(attrs map[string]any, key string) any {
	if attrs == nil {
		return nil
	}
	if v, ok := attrs[key]; ok {
		return v
	}
	if spec := podTemplateSpec(attrs); spec != nil {
		if v, ok := spec[key]; ok {
			return v
		}
	}
	return nil
}

func filterEligibleNodes(nodes []*graph.Node, w *graph.Node) []*graph.Node {
	var out []*graph.Node
	for _, n := range nodes {
		if nodeEligibleForWorkload(n, w) {
			out = append(out, n)
		}
	}
	return out
}

// nodeEligibleForWorkload mirrors kube-scheduler predicates we model: not cordoned,
// matches nodeSelector, matches required nodeAffinity, tolerates NoSchedule/NoExecute.
func nodeEligibleForWorkload(n, w *graph.Node) bool {
	if n == nil || w == nil {
		return false
	}
	if !nodeIsSchedulable(n) {
		return false
	}
	if sel := workloadNodeSelector(w); len(sel) > 0 {
		if !nodeMatchesSelector(n, sel) {
			return false
		}
	}
	if !nodeMatchesRequiredAffinity(n, workloadAffinity(w)) {
		return false
	}
	return nodeToleratedByWorkload(n, w)
}

func nodeIsSchedulable(n *graph.Node) bool {
	if n == nil || n.Attributes == nil {
		return true
	}
	if v, ok := n.Attributes["schedulable"]; ok {
		switch t := v.(type) {
		case bool:
			return t
		case string:
			return !strings.EqualFold(t, "false")
		}
	}
	return true
}

func workloadNodeSelector(w *graph.Node) map[string]any {
	if w == nil {
		return nil
	}
	if ns, ok := w.Attributes["nodeSelector"].(map[string]any); ok && len(ns) > 0 {
		return ns
	}
	if spec := podTemplateSpec(w.Attributes); spec != nil {
		if ns, ok := spec["nodeSelector"].(map[string]any); ok && len(ns) > 0 {
			return ns
		}
	}
	return nil
}

func workloadAffinity(w *graph.Node) map[string]any {
	if w == nil {
		return nil
	}
	if a, ok := w.Attributes["affinity"].(map[string]any); ok {
		return a
	}
	if spec := podTemplateSpec(w.Attributes); spec != nil {
		if a, ok := spec["affinity"].(map[string]any); ok {
			return a
		}
	}
	return nil
}

func workloadTolerations(w *graph.Node) []any {
	if w == nil {
		return nil
	}
	if t, ok := w.Attributes["tolerations"].([]any); ok {
		return t
	}
	if spec := podTemplateSpec(w.Attributes); spec != nil {
		if t, ok := spec["tolerations"].([]any); ok {
			return t
		}
	}
	return nil
}

func nodeMatchesRequiredAffinity(n *graph.Node, aff map[string]any) bool {
	if len(aff) == 0 {
		return true
	}
	na, _ := aff["nodeAffinity"].(map[string]any)
	if len(na) == 0 {
		return true
	}
	req, _ := na["requiredDuringSchedulingIgnoredDuringExecution"].(map[string]any)
	if len(req) == 0 {
		return true
	}
	terms, _ := req["nodeSelectorTerms"].([]any)
	if len(terms) == 0 {
		return true
	}
	labels := nodeLabels(n)
	for _, term := range terms {
		tm, _ := term.(map[string]any)
		if tm == nil {
			continue
		}
		if nodeSelectorTermMatches(n, labels, tm) {
			return true
		}
	}
	return false
}

func nodeSelectorTermMatches(n *graph.Node, labels map[string]any, term map[string]any) bool {
	exprs, _ := term["matchExpressions"].([]any)
	for _, e := range exprs {
		em, _ := e.(map[string]any)
		if em == nil || !matchExpressionMatches(labels, em) {
			return false
		}
	}
	fields, _ := term["matchFields"].([]any)
	for _, e := range fields {
		em, _ := e.(map[string]any)
		if em == nil {
			return false
		}
		// Only metadata.name is commonly used; treat unknown fields as non-match.
		key := fmt.Sprint(em["key"])
		if key != "metadata.name" {
			return false
		}
		name := ""
		if n != nil {
			name = n.Name
		}
		if !matchExpressionMatches(map[string]any{key: name}, map[string]any{
			"key": key, "operator": em["operator"], "values": em["values"],
		}) {
			return false
		}
	}
	return true
}

func matchExpressionMatches(labels map[string]any, expr map[string]any) bool {
	key := fmt.Sprint(expr["key"])
	op := fmt.Sprint(expr["operator"])
	vals, _ := expr["values"].([]any)
	lv, has := labels[key]
	ls := fmt.Sprint(lv)
	switch op {
	case "In":
		if !has {
			return false
		}
		for _, v := range vals {
			if ls == fmt.Sprint(v) {
				return true
			}
		}
		return false
	case "NotIn":
		if !has {
			return true
		}
		for _, v := range vals {
			if ls == fmt.Sprint(v) {
				return false
			}
		}
		return true
	case "Exists":
		return has
	case "DoesNotExist":
		return !has
	case "Gt", "Lt":
		if !has {
			return false
		}
		li, err1 := strconv.ParseInt(ls, 10, 64)
		if err1 != nil || len(vals) == 0 {
			return false
		}
		ri, err2 := strconv.ParseInt(fmt.Sprint(vals[0]), 10, 64)
		if err2 != nil {
			return false
		}
		if op == "Gt" {
			return li > ri
		}
		return li < ri
	default:
		return false
	}
}

func nodeToleratedByWorkload(n, w *graph.Node) bool {
	if n == nil {
		return false
	}
	raw, ok := n.Attributes["taints"].([]any)
	if !ok || len(raw) == 0 {
		return true
	}
	tols := workloadTolerations(w)
	for _, t := range raw {
		tm, _ := t.(map[string]any)
		if tm == nil {
			continue
		}
		effect := fmt.Sprint(tm["effect"])
		if effect == "" {
			effect = "NoSchedule"
		}
		if effect != "NoSchedule" && effect != "NoExecute" {
			continue
		}
		if !tolerationAllows(tols, tm, effect) {
			return false
		}
	}
	return true
}

func tolerationAllows(tols []any, taint map[string]any, effect string) bool {
	tKey := fmt.Sprint(taint["key"])
	tVal := fmt.Sprint(taint["value"])
	for _, tol := range tols {
		tolm, _ := tol.(map[string]any)
		if tolm == nil {
			continue
		}
		tolEffect := fmt.Sprint(tolm["effect"])
		if tolEffect != "" && tolEffect != effect {
			continue
		}
		op := fmt.Sprint(tolm["operator"])
		if op == "" {
			op = "Equal"
		}
		tolKey := fmt.Sprint(tolm["key"])
		// Empty key + Exists → tolerate everything.
		if tolKey == "" && strings.EqualFold(op, "Exists") {
			return true
		}
		if tolKey != tKey {
			continue
		}
		if strings.EqualFold(op, "Exists") {
			return true
		}
		if fmt.Sprint(tolm["value"]) == tVal {
			return true
		}
	}
	return false
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
