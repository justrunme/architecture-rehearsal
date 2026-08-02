package scenario

import (
	"fmt"
	"strings"
)

// PromZeroMatch detects rules that match zero series for a specific metric schema.
type PromZeroMatch struct{}

func (PromZeroMatch) Name() string { return "prom-zero-match" }

func (PromZeroMatch) Run(ctx Context) []Finding {
	ch := ctx.Change
	if ch == nil {
		return nil
	}
	if ch.EffectiveKind() != "prometheus-rule" && factString(ch.Facts, "scenario", "") != "prom-zero-match" {
		return nil
	}
	expr := factString(ch.Facts, "expr", factString(ch.Facts, "rule_expr", ""))
	if expr == "" {
		return nil
	}
	metric := factString(ch.Facts, "metric", guessMetric(expr))
	required := map[string]string{}
	if raw, ok := ch.Facts["label_matchers"].(map[string]any); ok {
		for k, v := range raw {
			if s, ok := v.(string); ok {
				required[k] = s
			}
		}
	}

	// metric_labels can be:
	// 1) metric-specific: { "kube_pod_status_ready": { "namespace": ["a"] } }
	// 2) legacy flat: { "namespace": ["a"] }
	labelsForMetric, knownMetric, legacy := metricLabels(ctx, metric)

	var missing []string
	zeroMatch := false

	if metric != "" && !legacy {
		if !knownMetric {
			missing = append(missing, fmt.Sprintf("metric %q not present in snapshot metric_labels", metric))
			zeroMatch = true
		}
	}
	for label, want := range required {
		vals := labelsForMetric[label]
		if len(vals) == 0 {
			missing = append(missing, fmt.Sprintf("label %q never observed on metric %q", label, metric))
			zeroMatch = true
			continue
		}
		if want != "" && !contains(vals, want) {
			missing = append(missing, fmt.Sprintf("label %s=%q not in observed values %v for metric %q", label, want, vals, metric))
			zeroMatch = true
		}
	}
	if !zeroMatch {
		return nil
	}
	return []Finding{{
		ID:       "prom-zero-match",
		Scenario: "prom-zero-match",
		Risk:     "high",
		Title:    "Alert rule matches zero observed series",
		Summary: fmt.Sprintf(
			"Rule is syntactically valid but selectors do not match observed label schema for metric %q. Expected alert coverage after deployment: 0%%. expr=%s",
			metric, truncate(expr, 120),
		),
		Components: ch.Seeds,
		Cascade: []string{
			"rule deploys successfully",
			"Prometheus accepts rule",
			"selector matches zero series",
			"alert never fires",
			"incident signal gap",
		},
		Controls: []string{
			"validate rule against metric-specific label values before merge",
			"align selector labels with actual metric schema",
			"require series-coverage check in CI",
		},
		SLOImpact:  "monitoring coverage gap — false sense of safety",
		Evidence:   missing,
		Rollback:   RollbackAvailable,
		Confidence: "high",
	}}
}

func metricLabels(ctx Context, metric string) (labels map[string][]string, known bool, legacy bool) {
	labels = map[string][]string{}
	if ctx.Baseline == nil || ctx.Baseline.Meta == nil {
		return labels, false, false
	}
	raw, ok := ctx.Baseline.Meta["metric_labels"].(map[string]any)
	if !ok {
		return labels, false, false
	}
	// Detect metric-specific vs flat: if first value is map → metric-specific.
	for _, v := range raw {
		if _, isMap := v.(map[string]any); isMap {
			// metric-specific
			if metric == "" {
				return labels, false, false
			}
			mv, ok := raw[metric].(map[string]any)
			if !ok {
				return labels, false, false
			}
			for lk, lv := range mv {
				labels[lk] = toStringSlice(lv)
			}
			return labels, true, false
		}
		break
	}
	// legacy flat
	for k, v := range raw {
		labels[k] = toStringSlice(v)
	}
	return labels, true, true
}

func toStringSlice(v any) []string {
	switch t := v.(type) {
	case []any:
		var out []string
		for _, x := range t {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
		return out
	case []string:
		return t
	default:
		return nil
	}
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

func guessMetric(expr string) string {
	expr = strings.TrimSpace(expr)
	i := strings.Index(expr, "{")
	if i > 0 {
		return strings.TrimSpace(expr[:i])
	}
	return ""
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
