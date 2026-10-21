package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/justrunme/architecture-rehearsal/internal/analyze"
	"github.com/justrunme/architecture-rehearsal/internal/argo"
	"github.com/justrunme/architecture-rehearsal/internal/change"
	"github.com/justrunme/architecture-rehearsal/internal/collect"
	"github.com/justrunme/architecture-rehearsal/internal/impact"
	"github.com/justrunme/architecture-rehearsal/internal/loader"
	"github.com/justrunme/architecture-rehearsal/internal/podtemplate"
	"github.com/justrunme/architecture-rehearsal/internal/presync"
	"github.com/justrunme/architecture-rehearsal/internal/rbac"
)

// cmdArgoPresync runs the change-impact gate as an Argo CD PreSync hook Job.
//
// Exit codes (hook contract): ALLOW/WARN → 0; BLOCK → 3; UNKNOWN → 4; errors → 5.
// Always prints one JSON decision line on stdout.
func cmdArgoPresync(args []string) int {
	start := time.Now()
	fail := func(decision string, reasons ...string) int {
		r := presync.Result{
			Decision:   decision,
			Reasons:    reasons,
			RuleIDs:    []string{},
			DurationMs: time.Since(start).Milliseconds(),
		}
		_ = presync.WriteJSONLine(os.Stdout, r)
		return presync.HookExitCode(decision)
	}

	if code := requireAction(rbac.ActionAnalyze); code != 0 {
		return fail(presync.Unknown, "authorization failed")
	}

	fs := flag.NewFlagSet("argo-presync", flag.ContinueOnError)
	app := fs.String("app", envOr("ARGOCD_APP_NAME", ""), "Argo CD Application name (default ARGOCD_APP_NAME)")
	appNS := fs.String("app-namespace", envOr("ARGOCD_APP_NAMESPACE", "argocd"), "Application namespace")
	revision := fs.String("revision", envOr("ARGOCD_APP_REVISION", ""), "target revision")
	desiredDir := fs.String("desired-dir", "", "pre-rendered desired manifests directory")
	baseline := fs.String("baseline", "", "baseline snapshot JSON")
	live := fs.Bool("live", false, "collect live baseline via kubectl; template compare uses apply --dry-run=server (needs patch)")
	cluster := fs.String("cluster", "cluster", "cluster name for live snapshot")
	namespace := fs.String("namespace", envOr("REHEARSAL_LIVE_NAMESPACE", ""), "limit manifest scope / live collect namespace (default REHEARSAL_LIVE_NAMESPACE)")
	allowRemove := fs.Bool("allow-remove", false, "allow removals within scope")
	changeID := fs.String("id", "", "change id")
	outDir := fs.String("out", "/tmp/argo-presync", "scratch output directory (emptyDir when read-only rootfs)")
	annotationsFile := fs.String("annotations-file", "", "Application annotations JSON object (tests / offline)")
	skipAppAnnot := fs.Bool("skip-app-annotations", false, "do not fetch Application annotations from the API")
	serverSideApply := fs.Bool("server-side-apply", false, "use SSA dry-run with field manager argocd-controller (Argo ServerSideApply=true)")
	timeout := fs.Duration("timeout", 120*time.Second, "maximum gate runtime (fail closed on exceed)")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return fail(presync.Unknown, "flag parse error: "+err.Error())
	}

	done := make(chan int, 1)
	go func() {
		done <- runArgoPresync(start, presyncArgs{
			app: *app, appNS: *appNS, revision: *revision,
			desiredDir: *desiredDir, baseline: *baseline, live: *live,
			cluster: *cluster, namespace: *namespace, allowRemove: *allowRemove,
			changeID: *changeID, outDir: *outDir,
			annotationsFile: *annotationsFile, skipAppAnnot: *skipAppAnnot,
			serverSideApply: *serverSideApply,
		})
	}()
	select {
	case code := <-done:
		return code
	case <-time.After(*timeout):
		return fail(presync.Unknown, fmt.Sprintf("gate timed out after %s", (*timeout).String()))
	}
}

type presyncArgs struct {
	app, appNS, revision       string
	desiredDir, baseline       string
	live                       bool
	cluster, namespace         string
	allowRemove                bool
	changeID, outDir           string
	annotationsFile            string
	skipAppAnnot               bool
	serverSideApply            bool
}

func runArgoPresync(start time.Time, a presyncArgs) int {
	emit := func(r presync.Result) int {
		r.DurationMs = time.Since(start).Milliseconds()
		if r.AppName == "" {
			r.AppName = a.app
		}
		_ = presync.WriteJSONLine(os.Stdout, r)
		code := presync.HookExitCode(r.Decision)
		fmt.Fprintf(os.Stderr, "presync decision=%s exit=%d durationMs=%d\n", r.Decision, code, r.DurationMs)
		if r.Override != nil && r.Override.Enabled {
			fmt.Fprintf(os.Stderr, "BREAK-GLASS OVERRIDE reason=%q\n", r.Override.Reason)
		}
		return code
	}
	fail := func(reasons ...string) int {
		return emit(presync.Result{Decision: presync.Unknown, Reasons: reasons, RuleIDs: []string{}})
	}

	if a.desiredDir == "" && a.app == "" {
		return fail("missing inputs: --desired-dir or --app is required")
	}
	if a.baseline == "" && !a.live {
		return fail("missing inputs: --baseline or --live is required")
	}
	if a.desiredDir != "" {
		if st, err := os.Stat(a.desiredDir); err != nil || !st.IsDir() {
			return fail(fmt.Sprintf("desired-dir unavailable: %v", err))
		}
	}

	// Break-glass annotations (fail closed on parse errors).
	ann, err := loadPresyncAnnotations(a)
	if err != nil {
		return fail("application annotations: " + err.Error())
	}
	ov, err := presync.ParseOverride(ann)
	if err != nil {
		return fail(err.Error())
	}

	if err := os.MkdirAll(a.outDir, 0o755); err != nil {
		return fail("out dir: " + err.Error())
	}

	basePath := a.baseline
	if basePath == "" {
		basePath = filepath.Join(a.outDir, "baseline.json")
		liveOpts := collect.LiveOptions{Cluster: a.cluster}
		if a.namespace != "" {
			for _, p := range strings.Split(a.namespace, ",") {
				p = strings.TrimSpace(p)
				if p != "" {
					liveOpts.Namespace = p
					break
				}
			}
		}
		snap, err := collect.K8sFromLive(nil, liveOpts)
		if err != nil {
			return fail("live baseline: " + err.Error())
		}
		raw, _ := json.MarshalIndent(snap, "", "  ")
		if err := os.WriteFile(basePath, raw, 0o644); err != nil {
			return fail("write baseline: " + err.Error())
		}
	}
	base, err := loader.LoadSnapshot(basePath)
	if err != nil {
		return fail("baseline: " + err.Error())
	}

	opts := argo.CompileOptions{
		AppName:      a.app,
		AppNamespace: a.appNS,
		Revision:     a.revision,
		ChangeID:     a.changeID,
		Scope:        change.ManifestScope{AllowRemove: a.allowRemove},
	}
	if a.namespace != "" {
		for _, p := range strings.Split(a.namespace, ",") {
			p = strings.TrimSpace(p)
			if p != "" {
				opts.Scope.Namespaces = append(opts.Scope.Namespaces, p)
			}
		}
	}

	var ch *loader.ChangeEnvelope
	if a.desiredDir != "" {
		opts.DesiredDir = a.desiredDir
		opts.Source = argo.SourceDirectory
		ch, err = argo.Compile(base, opts)
	} else {
		wd := filepath.Join(a.outDir, "work")
		_ = os.MkdirAll(wd, 0o755)
		fetcher := argo.NewCLIFetcherFromEnv()
		opts.Source = argo.SourceArgocdCLI
		ch, err = argo.FetchAndCompile(base, fetcher, argo.ApplicationRef{
			Name: a.app, Namespace: a.appNS, Revision: a.revision,
		}, wd, opts)
	}
	if err != nil {
		return fail("argo compile: " + err.Error())
	}
	if a.live {
		desiredForCompare := a.desiredDir
		if desiredForCompare == "" {
			desiredForCompare = filepath.Join(a.outDir, "work", "desired")
		}
		if desiredForCompare != "" {
			mode := podtemplate.ParseApplyModeFromEnv()
			if a.serverSideApply {
				mode = podtemplate.ApplyServerSide
			}
			if err := argo.ApplyLiveTemplateCompare(nil, ch, desiredForCompare, podtemplate.KubectlOptions{Mode: mode}); err != nil {
				return fail("live template compare: " + err.Error())
			}
		}
	}

	imp := impact.Run(base, ch, nil)
	an, err := analyze.RunWithRunners(base, ch, impact.ScenarioRunners())
	if err != nil {
		return fail("analyze: " + err.Error())
	}
	impact.MergeAnalyze(imp, an.Decision, an.Risk)

	r := presync.FromImpact(imp, time.Since(start))
	if r.Decision == "" {
		return fail("unknown decision from impact gate")
	}
	r = presync.ApplyOverride(r, ov)
	return emit(r)
}

func loadPresyncAnnotations(a presyncArgs) (map[string]string, error) {
	if a.annotationsFile != "" {
		raw, err := os.ReadFile(a.annotationsFile)
		if err != nil {
			return nil, err
		}
		var m map[string]string
		if err := json.Unmarshal(raw, &m); err != nil {
			return nil, fmt.Errorf("annotations-file: %w", err)
		}
		return m, nil
	}
	if a.skipAppAnnot || a.app == "" {
		return map[string]string{}, nil
	}
	return fetchAppAnnotations(a.app, a.appNS)
}

func fetchAppAnnotations(name, ns string) (map[string]string, error) {
	if name == "" {
		return nil, fmt.Errorf("application name required")
	}
	if ns == "" {
		ns = "argocd"
	}
	kubectl := os.Getenv("KUBECTL")
	if kubectl == "" {
		kubectl = "kubectl"
	}
	cmd := exec.Command(kubectl, "-n", ns, "get", "application", name, "-o", "json")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("kubectl get application %s/%s: %w", ns, name, err)
	}
	var obj struct {
		Metadata struct {
			Annotations map[string]string `json:"annotations"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(out, &obj); err != nil {
		return nil, err
	}
	if obj.Metadata.Annotations == nil {
		return map[string]string{}, nil
	}
	return obj.Metadata.Annotations, nil
}

func envOr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}
