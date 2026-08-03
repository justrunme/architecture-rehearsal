package scenario

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/justrunme/architecture-rehearsal/internal/graph"
)

type CNICapacity struct{}

func (CNICapacity) Name() string { return "cni-ip-capacity" }

func (CNICapacity) Applicable(ctx Context) bool {
	ch := ctx.Change
	if ch == nil {
		return false
	}
	k := changeKind(ch)
	if k == "scale-up" || k == "helm-upgrade" || k == "terraform-plan" || k == "k8s-manifest" {
		return true
	}
	return factString(ch.Facts, "scenario", "") == "cni-ip-capacity"
}

func (CNICapacity) MissingRequirements(ctx Context) []Requirement {
	var miss []Requirement
	if !hasKind(ctx.BaseIdx, graph.KindWorkload) {
		miss = append(miss, Requirement{ID: "workload", Message: "no Workload nodes for replica comparison"})
	}
	available := -1
	if ctx.Baseline != nil && ctx.Baseline.Meta != nil {
		available = factInt(ctx.Baseline.Meta, "pod_ip_capacity_available", -1)
	}
	if available < 0 {
		hasAlloc := false
		if ctx.BaseIdx != nil {
			for _, n := range ctx.BaseIdx.ByKind[graph.KindNode] {
				if n.AttrInt("allocatablePods") > 0 {
					hasAlloc = true
					break
				}
			}
		}
		if !hasAlloc {
			miss = append(miss, Requirement{ID: "pod_ip_capacity", Message: "no pod_ip_capacity_available meta and no node allocatablePods"})
		}
	}
	return miss
}

func (r CNICapacity) Evaluate(ctx Context) Result {
	ch := ctx.Change
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
		if rep <= baseR {
			continue
		}
		surge += parseMaxSurge(n.AttrString("maxSurge"), rep)
	}
	if v := factInt(ch.Facts, "rollout_surge", -1); v >= 0 {
		surge = v
	}
	requested := delta + surge
	if requested == 0 {
		return Result{Outcome: OutcomeNotMatched}
	}
	available := factInt(ctx.Baseline.Meta, "pod_ip_capacity_available", -1)
	if available < 0 {
		alloc := 0
		for _, n := range ctx.BaseIdx.ByKind[graph.KindNode] {
			alloc += n.AttrInt("allocatablePods")
		}
		available = alloc - baseRep
		if available < 0 {
			available = 0
		}
	}
	if requested <= available {
		return Result{Outcome: OutcomeNotMatched}
	}
	deficit := requested - available
	risk := "high"
	if deficit >= maxInt(1, requested/2) || deficit >= 10 {
		risk = "critical"
	}
	rb := RollbackUnknown
	if ch.Facts != nil {
		if v, ok := ch.Facts["rollback_available"].(bool); ok {
			if v {
				rb = RollbackAvailable
			} else {
				rb = RollbackUnavailable
			}
		}
	}
	return Result{Outcome: OutcomeMatched, Findings: []Finding{{
		ID: r.Name(), Scenario: r.Name(), Risk: risk,
		Title: "Pod IP capacity exhaustion (CNI / VPC)",
		Summary: fmt.Sprintf("Replica delta %d + surge %d = %d IPs needed; available %d (deficit %d). baseline=%d proposed=%d",
			delta, surge, requested, available, deficit, baseRep, propRep),
		Components: ch.Seeds,
		Cascade: []string{
			fmt.Sprintf("baseline_replicas=%d", baseRep),
			fmt.Sprintf("proposed_replicas=%d", propRep),
			fmt.Sprintf("rollout_surge=%d", surge),
			fmt.Sprintf("requested_ips=%d available=%d deficit=%d", requested, available, deficit),
			"IP exhaustion → FailedCreatePodSandbox → readiness timeout → Helm pending-install",
		},
		Controls: []string{"increase max-pods / prefix delegation", "add nodes", "reduce maxSurge"},
		SLOImpact: "rollout latency", Evidence: []string{
			fmt.Sprintf("baseline_replicas=%d", baseRep), fmt.Sprintf("proposed_replicas=%d", propRep),
			fmt.Sprintf("surge=%d", surge), fmt.Sprintf("available=%d", available),
		},
		Rollback: rb, Confidence: "high",
	}}}
}

// parseMaxSurge implements Kubernetes ceil for percentage maxSurge.
func parseMaxSurge(ms string, replicas int) int {
	if ms == "" {
		// default 25% ceil
		return int(math.Max(1, math.Ceil(float64(replicas)*0.25)))
	}
	if strings.HasSuffix(ms, "%") {
		p, _ := strconv.Atoi(strings.TrimSuffix(ms, "%"))
		return int(math.Max(1, math.Ceil(float64(replicas)*float64(p)/100.0)))
	}
	v, _ := strconv.Atoi(ms)
	if v < 0 {
		return 0
	}
	return v
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
