package scenario

import (
	"fmt"
	"strings"
)

type PromZeroMatch struct{}

func (PromZeroMatch) Name() string { return "prom-zero-match" }

func (PromZeroMatch) Applicable(ctx Context) bool {
	ch := ctx.Change
	if ch == nil {
		return false
	}
	return changeKind(ch) == "prometheus-rule" || factString(ch.Facts, "scenario", "") == "prom-zero-match"
}

func (PromZeroMatch) MissingRequirements(ctx Context) []Requirement {
	if ctx.Baseline == nil || ctx.Baseline.Meta == nil || ctx.Baseline.Meta["metric_labels"] == nil {
		return []Requirement{{ID: "metric_labels", Message: "meta.metric_labels required for Prometheus coverage check"}}
	}
	return nil
}

func (r PromZeroMatch) Evaluate(ctx Context) Result {
	ch := ctx.Change
	expr := factString(ch.Facts, "expr", factString(ch.Facts, "rule_expr", ""))
	if expr == "" {
		return Result{Outcome: OutcomeUnknown, Missing: []Requirement{{ID: "expr", Message: "rule expr missing"}}, Findings: []Finding{{
			ID: "unknown:prom-expr", Scenario: r.Name(), Risk: "unknown", Title: "Prometheus rule expr missing",
			Summary: "Cannot evaluate series coverage without expr/label_matchers", Rollback: RollbackUnknown, Confidence: "low",
		}}}
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
	labelsForMetric, knownMetric, legacy := metricLabels(ctx, metric)
	var missing []string
	zero := false
	if metric != "" && !legacy && !knownMetric {
		missing = append(missing, fmt.Sprintf("metric %q not in metric_labels", metric))
		zero = true
	}
	for label, want := range required {
		vals := labelsForMetric[label]
		if len(vals) == 0 {
			missing = append(missing, fmt.Sprintf("label %q never observed on metric %q", label, metric))
			zero = true
			continue
		}
		if want != "" && !contains(vals, want) {
			missing = append(missing, fmt.Sprintf("%s=%q not in %v for %q", label, want, vals, metric))
			zero = true
		}
	}
	if !zero {
		return Result{Outcome: OutcomeNotMatched}
	}
	return Result{Outcome: OutcomeMatched, Findings: []Finding{{
		ID: r.Name(), Scenario: r.Name(), Risk: "high",
		Title: "Alert rule matches zero observed series",
		Summary: fmt.Sprintf("Selectors do not match observed schema for metric %q; expected coverage 0%%. expr=%s", metric, truncate(expr, 100)),
		Components: ch.Seeds, Cascade: []string{"rule deploys", "matches zero series", "alert never fires"},
		Controls: []string{"validate metric-specific labels before merge", "series-coverage CI check"},
		SLOImpact: "monitoring gap", Evidence: missing, Rollback: RollbackAvailable, Confidence: "high",
	}}}
}

func metricLabels(ctx Context, metric string) (map[string][]string, bool, bool) {
	labels := map[string][]string{}
	if ctx.Baseline == nil || ctx.Baseline.Meta == nil {
		return labels, false, false
	}
	raw, ok := ctx.Baseline.Meta["metric_labels"].(map[string]any)
	if !ok {
		return labels, false, false
	}
	metricSpecific := false
	for _, v := range raw {
		if _, isMap := v.(map[string]any); isMap {
			metricSpecific = true
			break
		}
	}
	if metricSpecific {
		mv, ok := raw[metric].(map[string]any)
		if !ok {
			return labels, false, false
		}
		for lk, lv := range mv {
			labels[lk] = toStringSlice(lv)
		}
		return labels, true, false
	}
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
