package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/justrunme/architecture-rehearsal/internal/analyze"
	"github.com/justrunme/architecture-rehearsal/internal/argo"
	"github.com/justrunme/architecture-rehearsal/internal/change"
	"github.com/justrunme/architecture-rehearsal/internal/collect"
	"github.com/justrunme/architecture-rehearsal/internal/evidence"
	"github.com/justrunme/architecture-rehearsal/internal/impact"
	"github.com/justrunme/architecture-rehearsal/internal/integrations/status"
	"github.com/justrunme/architecture-rehearsal/internal/loader"
	"github.com/justrunme/architecture-rehearsal/internal/podtemplate"
	"github.com/justrunme/architecture-rehearsal/internal/rbac"
)

// cmdArgoImpact: native Argo CD change-impact gate.
//
//	rehearsal argo-impact --app NAME [--revision REV] [--baseline FILE|--live] ...
//	rehearsal argo-impact --desired-dir DIR --baseline FILE   # offline / CI fixtures
func cmdArgoImpact(args []string) int {
	if code := requireAction(rbac.ActionAnalyze); code != 0 {
		return code
	}
	fs := flag.NewFlagSet("argo-impact", flag.ContinueOnError)
	app := fs.String("app", "", "Argo CD Application name")
	appNS := fs.String("app-namespace", "", "Argo CD Application namespace (argocd)")
	revision := fs.String("revision", "", "target revision for desired manifests")
	desiredDir := fs.String("desired-dir", "", "pre-rendered desired manifests (offline; skips argocd CLI)")
	baseline := fs.String("baseline", "", "baseline snapshot JSON (live cluster graph)")
	live := fs.Bool("live", false, "collect live baseline via Kubernetes API")
	cluster := fs.String("cluster", "cluster", "cluster name for live/dump snapshot")
	dumpDir := fs.String("dump-dir", "", "optional kubectl dump directory for baseline")
	kubeconfig := fs.String("kubeconfig", "", "kubeconfig path for --live / apply dry-run")
	kubeContext := fs.String("context", "", "kube context for --live / apply dry-run")
	liveNamespace := fs.String("live-namespace", envOr("REHEARSAL_LIVE_NAMESPACE", ""), "limit namespaced live inventory to this namespace (Role RBAC); Nodes and Pods are still fetched cluster-wide. Distinct from --namespace (change scope only — does not narrow inventory)")
	serverSideApply := fs.Bool("server-side-apply", false, "use SSA dry-run with field manager argocd-controller (Argo ServerSideApply=true); default is client-side three-way apply dry-run")
	outDir := fs.String("out", "out/argo-impact", "output directory for report + evidence")
	namespace := fs.String("namespace", "", "limit manifest scope to namespace(s), comma-separated")
	allowRemove := fs.Bool("allow-remove", false, "allow removals within scope")
	changeID := fs.String("id", "", "change id (default argo-<app>)")
	title := fs.String("title", "", "change title")
	quiet := fs.Bool("quiet", false, "compact summary only")
	postStatus := fs.Bool("post-status", false, "post GitHub/GitLab commit status when env configured")
	statusErrors := fs.String("status-errors", "fail", "how to treat commit-status/check-run post failures: fail|warn")
	sha := fs.String("sha", "", "commit sha for status (defaults GITHUB_SHA / CI_COMMIT_SHA)")
	workdir := fs.String("workdir", "", "scratch dir for fetched manifests (default under --out)")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *app == "" && *desiredDir == "" {
		fmt.Fprintln(os.Stderr, "error: --app or --desired-dir is required")
		return 2
	}
	if *baseline == "" && !*live && *dumpDir == "" {
		fmt.Fprintln(os.Stderr, "error: --baseline, --live, or --dump-dir is required")
		return 2
	}

	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 5
	}
	wd := *workdir
	if wd == "" {
		wd = filepath.Join(*outDir, "work")
	}
	_ = os.MkdirAll(wd, 0o755)

	// --- baseline (live snapshot) ---
	var basePath string
	if *baseline != "" {
		basePath = *baseline
	} else {
		basePath = filepath.Join(*outDir, "baseline.json")
		if *dumpDir != "" {
			snap, err := collect.K8sFromManifests(nil, *dumpDir, collect.K8sOptions{ClusterName: *cluster})
			if err != nil {
				fmt.Fprintf(os.Stderr, "baseline dump: %v\n", err)
				return 5
			}
			raw, _ := json.MarshalIndent(snap, "", "  ")
			if err := os.WriteFile(basePath, raw, 0o644); err != nil {
				return 5
			}
		} else if *live {
			// Inventory namespace is independent of change scope (--namespace).
			// Only --live-namespace / REHEARSAL_LIVE_NAMESPACE narrows namespaced
			// inventory. Nodes and Pods are always fetched cluster-wide inside
			// K8sFromLive so scheduling-capacity sees cross-namespace pressure.
			snap, err := collect.K8sFromLive(nil, collect.LiveOptions{
				Cluster: *cluster, Kubeconfig: *kubeconfig, Context: *kubeContext,
				Namespace: *liveNamespace,
			})
			if err != nil {
				fmt.Fprintf(os.Stderr, "baseline live: %v\n", err)
				return 5
			}
			raw, _ := json.MarshalIndent(snap, "", "  ")
			if err := os.WriteFile(basePath, raw, 0o644); err != nil {
				return 5
			}
		}
	}
	base, err := loader.LoadSnapshot(basePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "baseline: %v\n", err)
		return 2
	}

	// --- desired manifests ---
	opts := argo.CompileOptions{
		AppName:      *app,
		AppNamespace: *appNS,
		Revision:     *revision,
		ChangeID:     *changeID,
		Title:        *title,
		Scope: change.ManifestScope{
			AllowRemove: *allowRemove,
		},
	}
	if *namespace != "" {
		for _, p := range strings.Split(*namespace, ",") {
			p = strings.TrimSpace(p)
			if p != "" {
				opts.Scope.Namespaces = append(opts.Scope.Namespaces, p)
			}
		}
	}

	var ch *loader.ChangeEnvelope
	if *desiredDir != "" {
		opts.DesiredDir = *desiredDir
		opts.Source = argo.SourceDirectory
		ch, err = argo.Compile(base, opts)
	} else {
		fetcher := argo.NewCLIFetcherFromEnv()
		opts.Source = argo.SourceArgocdCLI
		ch, err = argo.FetchAndCompile(base, fetcher, argo.ApplicationRef{
			Name: *app, Namespace: *appNS, Revision: *revision,
		}, wd, opts)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "argo compile: %v\n", err)
		return 5
	}
	// When talking to a live cluster, refine template-change detection with
	// apply dry-run matching the sync tool (client-side three-way by default).
	desiredForCompare := *desiredDir
	if desiredForCompare == "" {
		desiredForCompare = filepath.Join(wd, "desired")
	}
	if *live && desiredForCompare != "" {
		mode := podtemplate.ParseApplyModeFromEnv()
		if *serverSideApply {
			mode = podtemplate.ApplyServerSide
		}
		if err := argo.ApplyLiveTemplateCompare(nil, ch, desiredForCompare, podtemplate.KubectlOptions{
			Kubeconfig: *kubeconfig, Context: *kubeContext, Mode: mode,
		}); err != nil {
			fmt.Fprintf(os.Stderr, "live template compare: %v\n", err)
			return 5
		}
	}

	changePath := filepath.Join(*outDir, "change.json")
	rawCh, _ := json.MarshalIndent(ch, "", "  ")
	if err := os.WriteFile(changePath, rawCh, 0o644); err != nil {
		return 5
	}

	// --- impact detectors + analyze (with Argo scenario bridges) ---
	imp := impact.Run(base, ch, nil)
	// Analyze with Argo impact scenario bridges only (avoid CNI unknown on argo-sync).
	an, err := analyze.RunWithRunners(base, ch, impact.ScenarioRunners())
	if err != nil {
		fmt.Fprintf(os.Stderr, "analyze: %v\n", err)
		return 2
	}
	impact.MergeAnalyze(imp, an.Decision, an.Risk)

	mdPath := filepath.Join(*outDir, "argo-impact.md")
	jsonPath := filepath.Join(*outDir, "argo-impact.json")
	if err := impact.WriteMarkdown(mdPath, imp); err != nil {
		fmt.Fprintf(os.Stderr, "report md: %v\n", err)
		return 5
	}
	if err := impact.WriteJSON(jsonPath, imp); err != nil {
		fmt.Fprintf(os.Stderr, "report json: %v\n", err)
		return 5
	}

	evDir, err := evidence.Bundle(*outDir, an, abs(basePath), abs(changePath))
	if err != nil {
		fmt.Fprintf(os.Stderr, "evidence: %v\n", err)
		return 5
	}

	code := impact.ExitCode(imp.Verdict)
	if *postStatus {
		commit := *sha
		if commit == "" {
			commit = os.Getenv("GITHUB_SHA")
		}
		if commit == "" {
			commit = os.Getenv("CI_COMMIT_SHA")
		}
		if commit != "" {
			st, conclusion := status.MapDecision(impact.ToAnalyzeDecision(imp.Verdict), code)
			desc := fmt.Sprintf("argo-impact %s", imp.Verdict)
			warnOnly := strings.EqualFold(*statusErrors, "warn")
			handleStatusErr := func(what string, err error) int {
				if err == nil {
					return 0
				}
				msg := fmt.Sprintf("status post %s failed: %v", what, err)
				if warnOnly {
					fmt.Fprintf(os.Stderr, "WARNING: %s\n", msg)
					return 0
				}
				fmt.Fprintf(os.Stderr, "%s\n", msg)
				return 5
			}
			if g, err := status.FromEnvGitHub(); err == nil {
				if ec := handleStatusErr("github commit status", g.PostCommitStatus(commit, status.Status{State: st, Description: desc, Context: "architecture-rehearsal/argo-impact"})); ec != 0 {
					return ec
				}
				if ec := handleStatusErr("github check run", g.PostCheckRun(commit, "Argo CD impact gate", conclusion, impact.CheckSummary(imp))); ec != 0 {
					return ec
				}
			} else if err != nil && !warnOnly {
				fmt.Fprintf(os.Stderr, "status post github client: %v\n", err)
			}
			if gl, err := status.FromEnvGitLab(); err == nil {
				if ec := handleStatusErr("gitlab commit status", gl.PostCommitStatus(commit, status.Status{State: st, Description: desc, Context: "architecture-rehearsal/argo-impact"})); ec != 0 {
					return ec
				}
			}
		}
	}

	if *quiet {
		fmt.Printf("verdict=%s decision=%s findings=%d confidence=%s evidence=%s md=%s\n",
			imp.Verdict, an.Decision, len(an.Findings), imp.Confidence, evDir, mdPath)
	} else {
		fmt.Print(impact.Markdown(imp))
		fmt.Fprintf(os.Stderr, "json: %s\nmarkdown: %s\nevidence: %s\n", jsonPath, mdPath, evDir)
	}
	return code
}
