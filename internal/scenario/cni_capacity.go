package scenario

import (
	"fmt"

	"github.com/justrunme/architecture-rehearsal/internal/loader"
)

// CNICapacity detects AWS VPC CNI (or similar) pod IP exhaustion leading to
// FailedCreatePodSandbox → readiness timeout → Helm pending-install.
type CNICapacity struct{}

func (CNICapacity) Name() string { return "cni-ip-capacity" }

func (CNICapacity) Run(ctx Context) []Finding {
	ch := ctx.Change
	if ch == nil {
		return nil
	}
	if ch.Kind != "scale-up" && ch.Kind != "helm-upgrade" && ch.Kind != "terraform-plan" {
		if loaderFactString(ch.Facts, "scenario", "") != "cni-ip-capacity" {
			return nil
		}
	}

	// Prefer change facts; fall back to baseline meta.
	meta := map[string]any{}
	if ctx.Baseline != nil && ctx.Baseline.Meta != nil {
		for k, v := range ctx.Baseline.Meta {
			meta[k] = v
		}
	}
	if ch.Facts != nil {
		for k, v := range ch.Facts {
			meta[k] = v
		}
	}

	requested := loader.FactInt(meta, "pods_requested", 0)
	available := loader.FactInt(meta, "pod_ip_capacity_available", 0)
	if requested == 0 {
		// Derive from replica delta if present.
		requested = loader.FactInt(meta, "replica_delta", 0)
	}
	if requested <= 0 || available < 0 {
		return nil
	}
	if requested <= available {
		return nil // no exhaustion
	}
	deficit := requested - available

	cascade := []string{
		fmt.Sprintf("pods requested: %d", requested),
		fmt.Sprintf("available pod IP capacity: %d", available),
		fmt.Sprintf("predicted unschedulable / sandbox failures: %d", deficit),
		"IP exhaustion",
		"FailedCreatePodSandbox",
		"delayed pod startup",
		"readiness probe timeouts",
		"Helm pending-install / upgrade timeout",
		"deployment job failure",
	}

	comps := append([]string{}, ch.Seeds...)
	risk := "high"
	if deficit >= requested/2 || deficit >= 10 {
		risk = "critical"
	}

	return []Finding{{
		ID:       "cni-ip-capacity",
		Scenario: "cni-ip-capacity",
		Risk:     risk,
		Title:    "Pod IP capacity exhaustion (CNI / VPC)",
		Summary: fmt.Sprintf(
			"Change requests %d new pods but only %d pod IPs remain. Predict %d FailedCreatePodSandbox events and Helm install timeout risk.",
			requested, available, deficit,
		),
		Components: comps,
		Cascade:    cascade,
		Controls: []string{
			"increase max-pods / prefix delegation before scale",
			"add nodes or warm IP inventory",
			"lower replica surge / maxUnavailable carefully",
			"extend Helm --timeout only after capacity fix (not as primary fix)",
			"validate AWS VPC CNI warm pool settings",
		},
		SLOImpact:  "rollout latency / availability during deploy window",
		Evidence:   []string{fmt.Sprintf("pods_requested=%d", requested), fmt.Sprintf("pod_ip_capacity_available=%d", available)},
		RollbackOK: loaderFactBool(ch.Facts, "rollback_available", true),
		Confidence: "high",
	}}
}
