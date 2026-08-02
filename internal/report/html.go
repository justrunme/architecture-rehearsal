package report

import (
	"fmt"
	"html"
	"os"
	"strings"
	"time"

	"github.com/justrunme/architecture-rehearsal/internal/analyze"
)

// WriteHTML writes a self-contained impact report.
func WriteHTML(path string, r *analyze.Report) error {
	var b strings.Builder
	b.WriteString(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8"/>
<meta name="viewport" content="width=device-width, initial-scale=1"/>
<title>Architecture Rehearsal — Impact Report</title>
<style>
:root { --bg:#0b1220; --card:#121a2b; --text:#e8eefc; --muted:#93a0b8; --crit:#ff5c7a; --high:#ffb020; --med:#f5d76e; --low:#5bd6a2; --line:#243047; }
* { box-sizing: border-box; }
body { margin:0; font-family: ui-sans-serif, system-ui, -apple-system, Segoe UI, Roboto, sans-serif; background:var(--bg); color:var(--text); line-height:1.5; }
header { padding:28px 32px; border-bottom:1px solid var(--line); background:linear-gradient(180deg,#152038,#0b1220); }
h1 { margin:0 0 6px; font-size:1.55rem; letter-spacing:-0.02em; }
.sub { color:var(--muted); font-size:0.95rem; }
.badge { display:inline-block; padding:4px 10px; border-radius:999px; font-weight:700; font-size:0.8rem; text-transform:uppercase; letter-spacing:0.04em; }
.risk-critical { background:rgba(255,92,122,.18); color:var(--crit); border:1px solid rgba(255,92,122,.35); }
.risk-high { background:rgba(255,176,32,.15); color:var(--high); border:1px solid rgba(255,176,32,.35); }
.risk-medium { background:rgba(245,215,110,.12); color:var(--med); border:1px solid rgba(245,215,110,.3); }
.risk-low, .risk-none { background:rgba(91,214,162,.12); color:var(--low); border:1px solid rgba(91,214,162,.3); }
main { padding:24px 32px 48px; max-width:1100px; margin:0 auto; }
.grid { display:grid; grid-template-columns:repeat(auto-fit,minmax(160px,1fr)); gap:12px; margin:18px 0 28px; }
.card { background:var(--card); border:1px solid var(--line); border-radius:14px; padding:14px 16px; }
.card .k { color:var(--muted); font-size:0.78rem; text-transform:uppercase; letter-spacing:0.05em; }
.card .v { font-size:1.35rem; font-weight:700; margin-top:4px; }
.finding { background:var(--card); border:1px solid var(--line); border-radius:14px; padding:18px 18px 14px; margin:0 0 16px; }
.finding h3 { margin:0 0 8px; font-size:1.05rem; }
.muted { color:var(--muted); }
ol.cascade { margin:8px 0 0 18px; padding:0; }
ol.cascade li { margin:4px 0; }
ul.controls { margin:8px 0 0 18px; }
.footer { margin-top:32px; color:var(--muted); font-size:0.85rem; border-top:1px solid var(--line); padding-top:16px; }
code { background:#1a2438; padding:1px 6px; border-radius:6px; font-size:0.9em; }
</style>
</head>
<body>
<header>
  <h1>Architecture Rehearsal</h1>
  <div class="sub">Know what breaks before you deploy · v` + html.EscapeString(r.Version) + `</div>
  <p style="margin:14px 0 0">
    <span class="badge risk-` + html.EscapeString(r.Risk) + `">risk: ` + html.EscapeString(r.Risk) + `</span>
    &nbsp; <span class="badge risk-` + decisionClass(r.Decision) + `">decision: ` + html.EscapeString(r.Decision) + `</span>
  </p>
  <p class="sub" style="margin:12px 0 0">` + html.EscapeString(r.ChangeTitle) + ` · <code>` + html.EscapeString(r.ChangeID) + `</code> · baseline <code>` + html.EscapeString(r.BaselineID) + `</code></p>
</header>
<main>
  <p>` + html.EscapeString(r.Summary) + `</p>
  <div class="grid">
    <div class="card"><div class="k">Affected</div><div class="v">` + fmt.Sprintf("%d", r.AffectedComponents) + `</div></div>
    <div class="card"><div class="k">Critical paths</div><div class="v">` + fmt.Sprintf("%d", r.CriticalPaths) + `</div></div>
    <div class="card"><div class="k">SLO signals</div><div class="v">` + fmt.Sprintf("%d", r.SLOViolations) + `</div></div>
    <div class="card"><div class="k">Rollback</div><div class="v">` + boolYes(r.RollbackAvailable) + `</div></div>
  </div>
`)

	if len(r.PredictedFailures) > 0 {
		b.WriteString(`<h2>Predicted failures</h2><p>`)
		b.WriteString(html.EscapeString(strings.Join(r.PredictedFailures, ", ")))
		b.WriteString(`</p>`)
	}

	b.WriteString(`<h2>Findings</h2>`)
	if len(r.Findings) == 0 {
		b.WriteString(`<p class="muted">No deterministic scenarios matched. Review coverage gaps below.</p>`)
	}
	for _, f := range r.Findings {
		b.WriteString(`<article class="finding">`)
		b.WriteString(`<h3><span class="badge risk-` + html.EscapeString(f.Risk) + `">` + html.EscapeString(f.Risk) + `</span> ` + html.EscapeString(f.Title) + `</h3>`)
		b.WriteString(`<p>` + html.EscapeString(f.Summary) + `</p>`)
		if len(f.Cascade) > 0 {
			b.WriteString(`<div class="muted">Cascade</div><ol class="cascade">`)
			for _, step := range f.Cascade {
				b.WriteString(`<li>` + html.EscapeString(step) + `</li>`)
			}
			b.WriteString(`</ol>`)
		}
		if len(f.Controls) > 0 {
			b.WriteString(`<div class="muted" style="margin-top:10px">Recommended controls</div><ul class="controls">`)
			for _, c := range f.Controls {
				b.WriteString(`<li>` + html.EscapeString(c) + `</li>`)
			}
			b.WriteString(`</ul>`)
		}
		if len(f.Evidence) > 0 {
			b.WriteString(`<p class="muted" style="margin-top:10px">Evidence: <code>` + html.EscapeString(strings.Join(f.Evidence, "; ")) + `</code></p>`)
		}
		b.WriteString(`</article>`)
	}

	if len(r.CoverageGaps) > 0 {
		b.WriteString(`<h2>Coverage gaps</h2><ul class="controls">`)
		for _, g := range r.CoverageGaps {
			b.WriteString(`<li class="muted">` + html.EscapeString(g) + `</li>`)
		}
		b.WriteString(`</ul>`)
	}

	gen := r.Generated
	if gen.IsZero() {
		gen = time.Now().UTC()
	}
	b.WriteString(`<div class="footer">
    Graph and rules decide. AI (optional, later) only explains.<br/>
    Generated ` + html.EscapeString(gen.UTC().Format(time.RFC3339)) + ` · Architecture Rehearsal v0.1 · Apache-2.0
  </div>
</main>
</body>
</html>`)

	return os.WriteFile(path, []byte(b.String()), 0o644)
}

func boolYes(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}

func decisionClass(d string) string {
	switch d {
	case analyze.DecisionBlock:
		return "critical"
	case analyze.DecisionWarn:
		return "high"
	default:
		return "low"
	}
}
