package scenario

import (
	"fmt"
	"strings"

	"github.com/justrunme/architecture-rehearsal/internal/graph"
	"github.com/justrunme/architecture-rehearsal/internal/loader"
)

// PromZeroMatch detects Prometheus rules that are syntactically fine but
// match zero observed series given the label schema snapshot.
type PromZeroMatch struct{}

func (PromZeroMatch) Name() string { return "prom-zero-match" }

func (PromZeroMatch) Run(ctx Context) []Finding {
	ch := ctx.Change
	if ch == nil {
		return nil
	}
	if ch.Kind != "prometheus-rule" && loaderFactString(ch.Facts, "scenario", "") != "prom-zero-match" {
		return nil
	}

	expr := loader.FactString(ch.Facts, "expr", "")
	if expr == "" {
		expr = loader.FactString(ch.Facts, "rule_expr", "")
	}
	if expr == "" {
		return nil
	}

	required := map[string]string{}
	if raw, ok := ch.Facts["label_matchers"].(map[string]any); ok {
		for k, v := range raw {
			if s, ok := v.(string); ok {
				required[k] = s
			}
		}
	}
	if len(required) == 0 {
		if pairs, ok := ch.Facts["selectors"].([]any); ok {
			for _, p := range pairs {
				if m, ok := p.(map[string]any); ok {
					k, _ := m["label"].(string)
					v, _ := m["value"].(string)
					if k != "" {
						required[k] = v
					}
				}
			}
		}
	}

	observed := observedLabelValues(ctx)
	metric := loader.FactString(ch.Facts, "metric", "")
	if metric == "" {
		metric = guessMetric(expr)
	}

	var missing []string
	zeroMatch := false
	for label, want := range required {
		vals := observed[label]
		if len(vals) == 0 {
			missing = append(missing, fmt.Sprintf("label %q never observed on series", label))
			zeroMatch = true
			continue
		}
		if want != "" && !contains(vals, want) {
			missing = append(missing, fmt.Sprintf("label %s=%q not in observed values %v", label, want, vals))
			zeroMatch = true
		}
	}
	if metric != "" {
		if known, ok := observed["__metrics__"]; ok && len(known) > 0 && !contains(known, metric) {
			missing = append(missing, fmt.Sprintf("metric %q not present in snapshot", metric))
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
			"Rule is syntactically valid but selectors do not match observed label schema. Expected alert coverage after deployment: 0%%. expr=%s",
			truncate(expr, 120),
		),
		Components: ch.Seeds,
		Cascade: []string{
			"rule deploys successfully",
			"Prometheus accepts rule",
			"selector matches zero series",
			"alert never fires",
			"incident signal gap",
			"SLO burn may go undetected",
		},
		Controls: []string{
			"validate rule against /api/v1/labels and series API before merge",
			"align selector labels with actual metric schema",
			"add recording rule or relabel if cluster label is missing",
			"require promtool + series-coverage check in CI",
		},
		SLOImpact:  "monitoring coverage gap — false sense of safety",
		Evidence:   missing,
		RollbackOK: true,
		Confidence: "high",
	}}
}

func observedLabelValues(ctx Context) map[string][]string {
	out := map[string][]string{}
	if ctx.Baseline != nil && ctx.Baseline.Meta != nil {
		if raw, ok := ctx.Baseline.Meta["metric_labels"].(map[string]any); ok {
			for k, v := range raw {
				out[k] = toStringSlice(v)
			}
		}
		if raw, ok := ctx.Baseline.Meta["metrics"].([]any); ok {
			var metrics []string
			for _, m := range raw {
				if s, ok := m.(string); ok {
					metrics = append(metrics, s)
				}
			}
			if len(metrics) > 0 {
				out["__metrics__"] = metrics
			}
		}
	}
	if ctx.BaseIdx != nil {
		for _, n := range ctx.BaseIdx.ByKind[graph.KindAlert] {
			for k, v := range n.Attributes {
				if strings.HasPrefix(k, "label:") {
					lab := strings.TrimPrefix(k, "label:")
					if s, ok := v.(string); ok {
						out[lab] = appendUnique(out[lab], s)
					}
				}
			}
		}
	}
	return out
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

func appendUnique(ss []string, v string) []string {
	if contains(ss, v) {
		return ss
	}
	return append(ss, v)
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
