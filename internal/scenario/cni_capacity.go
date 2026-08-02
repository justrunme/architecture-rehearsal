package scenario

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/justrunme/architecture-rehearsal/internal/graph"
)

// CNICapacity detects pod IP exhaustion from baseline→proposed replica deltas.
type CNICapacity struct{}

func (CNICapacity) Name() string { return "cni-ip-capacity" }

func (CNICapacity) Run(ctx Context) []Finding {
	ch := ctx.Change
	if ch == nil || ctx.BaseIdx == nil || ctx.PropIdx == nil {
		return nil
	}
	kind := ch.EffectiveKind()
	if kind != "scale-up" && kind != "helm-upgrade" && kind != "terraform-plan" && kind != "k8s-manifest" {
		if factString(ch.Facts, "scenario", "") != "cni-ip-capacity" {
			return nil
		}
	}

	baseRep := graph.TotalWorkloadReplicas(ctx.BaseIdx)
	propRep := graph.TotalWorkloadReplicas(ctx.PropIdx)
	delta := propRep - baseRep
	if delta < 0 {
		delta = 0
	}

	// Rollout surge: sum maxSurge from patched/proposed workloads (default 25% or 1).
	surge := 0
	for _, n := range ctx.PropIdx.ByKind[graph.KindWorkload] {
		ms := n.AttrString("maxSurge")
		rep := n.WorkloadReplicas()
		if ms == "" {
			// only count surge for workloads that grew
			bn := ctx.BaseIdx.ByID[n.ID]
			baseR := 0
			if bn != nil {
				baseR = bn.WorkloadReplicas()
			}
			if rep > baseR {
				surge += maxInt(1, rep/4) // ~25%
			}
			continue
		}
		if strings.HasSuffix(ms, "%") {
			p, _ := strconv.Atoi(strings.TrimSuffix(ms, "%"))
			surge += maxInt(1, rep*p/100)
		} else {
			v, _ := strconv.Atoi(ms)
			surge += v
		}
	}
	// Allow explicit override for advanced tests only.
	if v := factInt(ch.Facts, "rollout_surge", -1); v >= 0 {
		surge = v
	}

	requested := delta + surge
	// Prefer derived; fall back to explicit only if no replica graph.
	if requested == 0 {
		requested = factInt(ch.Facts, "pods_requested", 0)
	}

	available := factInt(ctx.Baseline.Meta, "pod_ip_capacity_available", -1)
	if available < 0 {
		available = factInt(ch.Facts, "pod_ip_capacity_available", -1)
	}
	if available < 0 {
		// derive from node allocatablePods - current pods if present
		alloc := 0
		for _, n := range ctx.BaseIdx.ByKind[graph.KindNode] {
			alloc += n.AttrInt("allocatablePods")
		}
		if alloc > 0 {
			available = alloc - baseRep
			if available < 0 {
				available = 0
			}
		}
	}
	if requested <= 0 || available < 0 {
		return nil
	}
	if requested <= available {
		return nil
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
	return []Finding{{
		ID:       "cni-ip-capacity",
		Scenario: "cni-ip-capacity",
		Risk:     risk,
		Title:    "Pod IP capacity exhaustion (CNI / VPC)",
		Summary: fmt.Sprintf(
			"Replica delta %d + rollout surge %d = %d additional pod IPs needed; only %d available (deficit %d). baseline_replicas=%d proposed_replicas=%d",
			delta, surge, requested, available, deficit, baseRep, propRep,
		),
		Components: ch.Seeds,
		Cascade: []string{
			fmt.Sprintf("baseline workload replicas: %d", baseRep),
			fmt.Sprintf("proposed workload replicas: %d", propRep),
			fmt.Sprintf("rollout surge estimate: %d", surge),
			fmt.Sprintf("additional IPs requested: %d", requested),
			fmt.Sprintf("available pod IP capacity: %d", available),
			fmt.Sprintf("predicted sandbox failures: %d", deficit),
			"IP exhaustion → FailedCreatePodSandbox → readiness timeout → Helm pending-install",
		},
		Controls: []string{
			"increase max-pods / prefix delegation before scale",
			"add nodes or warm IP inventory",
			"reduce maxSurge during rollout",
			"extend Helm timeout only after capacity fix",
		},
		SLOImpact:  "rollout latency / availability during deploy window",
		Evidence:   []string{fmt.Sprintf("baseline_replicas=%d", baseRep), fmt.Sprintf("proposed_replicas=%d", propRep), fmt.Sprintf("surge=%d", surge), fmt.Sprintf("available=%d", available)},
		Rollback:   rb,
		Confidence: "high",
	}}
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
