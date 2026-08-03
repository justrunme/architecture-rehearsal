// rehearsal — production change-gate CLI for Architecture Rehearsal.
// Graph and rules decide. AI only explains (optional, not in risk path).
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/justrunme/architecture-rehearsal/internal/analyze"
	"github.com/justrunme/architecture-rehearsal/internal/change"
	"github.com/justrunme/architecture-rehearsal/internal/collect"
	"github.com/justrunme/architecture-rehearsal/internal/evidence"
	"github.com/justrunme/architecture-rehearsal/internal/graph"
	"github.com/justrunme/architecture-rehearsal/internal/loader"
	"github.com/justrunme/architecture-rehearsal/internal/rbac"
	"github.com/justrunme/architecture-rehearsal/internal/report"
	"github.com/justrunme/architecture-rehearsal/internal/store"
	"github.com/justrunme/architecture-rehearsal/internal/validate"
	"github.com/justrunme/architecture-rehearsal/internal/verify"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var code int
	switch os.Args[1] {
	case "analyze":
		code = cmdAnalyze(os.Args[2:])
	case "verify":
		code = cmdVerify(os.Args[2:])
	case "snapshot":
		code = cmdSnapshot(os.Args[2:])
	case "change":
		code = cmdChange(os.Args[2:])
	case "merge":
		code = cmdMerge(os.Args[2:])
	case "store":
		code = cmdStore(os.Args[2:])
	case "audit":
		code = cmdAudit(os.Args[2:])
	case "sign":
		code = cmdSign(os.Args[2:])
	case "verify-sign":
		code = cmdVerifySign(os.Args[2:])
	case "serve":
		code = cmdServe(os.Args[2:])
	case "run":
		code = cmdRun(os.Args[2:])
	case "evidence":
		code = cmdEvidence(os.Args[2:])
	case "policy":
		code = cmdPolicy(os.Args[2:])
	case "calibrate":
		code = cmdCalibrate(os.Args[2:])
	case "schemas":
		code = cmdSchemas(os.Args[2:])
	case "operator":
		code = cmdOperator(os.Args[2:])
	case "version":
		fmt.Println("rehearsal", analyze.Version)
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", os.Args[1])
		usage()
		code = 2
	}
	os.Exit(code)
}

func usage() {
	fmt.Fprintf(os.Stderr, `Architecture Rehearsal — deterministic pre-deploy simulation + post-deploy verification control plane

Pipeline:
  Collect → Model → Propose → Rehearse → Gate → Observe → Verify → Calibrate

Usage:
  rehearsal analyze  --baseline FILE --change FILE [--store DIR] [flags]
  rehearsal verify   --report FILE --observed FILE --baseline FILE --change FILE
  rehearsal snapshot k8s --dir MANIFESTS| --live [flags] --out FILE
  rehearsal change   manifests|terraform ...
  rehearsal run execute --baseline F --change F [--observed F] --out F
  rehearsal evidence chain|verify-chain|sign-dsse ...
  rehearsal policy   [--file policy.yaml] --risk high --decision block
  rehearsal calibrate [--demo]
  rehearsal schemas
  rehearsal serve    --workdir DIR [--addr :8080] [--db PATH|postgres://] [--blob DIR] [--async] [--workers N]
  rehearsal operator [--watch DIR] [--once]
  rehearsal merge|store|audit|sign|verify-sign ...
  rehearsal version

Exit codes: 0 approve/verified · 1 warn/diverged · 2 usage · 3 block · 4 unknown/inconclusive · 5 internal

Graph and rules decide. Missing data never becomes false approve.
verify without --baseline/--change is legacy (max INCONCLUSIVE).
`)
}

// loadAccessPolicy loads optional REHEARSAL_POLICY YAML (local access policy model).
func loadAccessPolicy() *rbac.Policy {
	path := os.Getenv("REHEARSAL_POLICY")
	p, err := rbac.LoadPolicy(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "policy: %v (continuing with default)\n", err)
		return rbac.DefaultPolicy()
	}
	return p
}

func requireAction(action rbac.Action) int {
	if err := loadAccessPolicy().Require(rbac.ActorFromEnv(), action); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 2
	}
	return 0
}

func cmdAnalyze(args []string) int {
	if code := requireAction(rbac.ActionAnalyze); code != 0 {
		return code
	}
	fs := flag.NewFlagSet("analyze", flag.ContinueOnError)
	baseline := fs.String("baseline", "", "baseline snapshot JSON")
	changePath := fs.String("change", "", "change envelope JSON")
	outDir := fs.String("out", "out", "evidence output directory")
	htmlPath := fs.String("html", "", "optional extra HTML path")
	quiet := fs.Bool("quiet", false, "compact summary only")
	storeRoot := fs.String("store", "", "optional run store directory (persists run + audit)")
	signOut := fs.String("sign-out", "", "optional path to write HMAC-signed evidence (needs REHEARSAL_HMAC_SECRET)")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *baseline == "" || *changePath == "" {
		fmt.Fprintln(os.Stderr, "error: --baseline and --change are required")
		return 2
	}
	base, err := loader.LoadSnapshot(*baseline)
	if err != nil {
		fmt.Fprintf(os.Stderr, "baseline: %v\n", err)
		return 2
	}
	ch, err := loader.LoadChange(*changePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "change: %v\n", err)
		return 2
	}
	if err := validate.ChangeAgainstBaseline(base, ch); err != nil {
		fmt.Fprintf(os.Stderr, "validate: %v\n", err)
		return 2
	}
	rep, err := analyze.Run(base, ch)
	if err != nil {
		fmt.Fprintf(os.Stderr, "analyze: %v\n", err)
		return 2
	}
	dir, err := evidence.Bundle(*outDir, rep, abs(*baseline), abs(*changePath))
	if err != nil {
		fmt.Fprintf(os.Stderr, "evidence: %v\n", err)
		return 5
	}
	if *htmlPath != "" {
		if err := report.WriteHTML(*htmlPath, rep); err != nil {
			fmt.Fprintf(os.Stderr, "html: %v\n", err)
			return 5
		}
	}
	if *storeRoot != "" {
		st, err := store.NewFS(*storeRoot)
		if err != nil {
			fmt.Fprintf(os.Stderr, "store: %v\n", err)
			return 5
		}
		path, err := st.SaveFromReport(rep, dir, rbac.ActorFromEnv())
		if err != nil {
			fmt.Fprintf(os.Stderr, "store save: %v\n", err)
			return 5
		}
		fmt.Fprintf(os.Stderr, "run stored: %s\n", path)
	}
	if *signOut != "" {
		sec := evidence.SecretFromEnv()
		if len(sec) == 0 {
			fmt.Fprintln(os.Stderr, "sign: REHEARSAL_HMAC_SECRET not set")
			return 2
		}
		env, err := evidence.SignReportHMAC(rep, sec, "env")
		if err != nil {
			fmt.Fprintf(os.Stderr, "sign: %v\n", err)
			return 5
		}
		raw, _ := json.MarshalIndent(env, "", "  ")
		if err := os.WriteFile(*signOut, raw, 0o644); err != nil {
			return 5
		}
		fmt.Fprintf(os.Stderr, "signed evidence: %s\n", *signOut)
	}
	if *quiet {
		fmt.Printf("risk=%s decision=%s findings=%d rollback=%s digest=%s evidence=%s\n",
			rep.Risk, rep.Decision, len(rep.Findings), rep.Rollback, rep.SemanticDigest, dir)
	} else {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(rep)
		fmt.Fprintf(os.Stderr, "evidence: %s\n", dir)
	}
	switch rep.Decision {
	case analyze.DecisionBlock:
		return 3
	case analyze.DecisionWarn:
		return 1
	case analyze.DecisionUnknown:
		return 4
	default:
		return 0
	}
}

func cmdVerify(args []string) int {
	if code := requireAction(rbac.ActionVerify); code != 0 {
		return code
	}
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	reportPath := fs.String("report", "", "prior analyze report.json")
	observed := fs.String("observed", "", "post-deploy snapshot JSON")
	baselinePath := fs.String("baseline", "", "baseline for production identity/delta (required for VERIFIED)")
	changePath := fs.String("change", "", "change envelope for production identity (required for VERIFIED)")
	out := fs.String("out", "", "write verification JSON")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *reportPath == "" || *observed == "" {
		fmt.Fprintln(os.Stderr, "error: --report and --observed required")
		return 2
	}
	if *baselinePath == "" || *changePath == "" {
		fmt.Fprintln(os.Stderr, "warn: without --baseline and --change, mode=legacy and max outcome is INCONCLUSIVE")
	}
	raw, err := os.ReadFile(*reportPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "report: %v\n", err)
		return 2
	}
	var pred analyze.Report
	if err := json.Unmarshal(raw, &pred); err != nil {
		fmt.Fprintf(os.Stderr, "report parse: %v\n", err)
		return 2
	}
	obs, err := loader.LoadSnapshot(*observed)
	if err != nil {
		fmt.Fprintf(os.Stderr, "observed: %v\n", err)
		return 2
	}
	opts := verify.Options{}
	if *baselinePath != "" {
		b, err := loader.LoadSnapshot(*baselinePath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "baseline: %v\n", err)
			return 2
		}
		opts.Baseline = b
	}
	if *changePath != "" {
		ch, err := loader.LoadChange(*changePath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "change: %v\n", err)
			return 2
		}
		opts.Change = ch
	}
	res := verify.RunWithOptions(&pred, obs, opts)
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(res)
	if *out != "" {
		b, _ := json.MarshalIndent(res, "", "  ")
		if err := os.WriteFile(*out, b, 0o644); err != nil {
			return 5
		}
	}
	switch res.Outcome {
	case verify.OutcomeVerified:
		return 0
	case verify.OutcomeInconclusive:
		return 4
	default:
		return 1
	}
}

func cmdSnapshot(args []string) int {
	if code := requireAction(rbac.ActionSnapshot); code != 0 {
		return code
	}
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: rehearsal snapshot k8s --dir DIR|--live --out FILE")
		return 2
	}
	switch args[0] {
	case "k8s":
		fs := flag.NewFlagSet("snapshot-k8s", flag.ContinueOnError)
		dir := fs.String("dir", "", "directory of Kubernetes YAML manifests")
		live := fs.Bool("live", false, "read-only live collect via kubectl (requires kubectl + kubeconfig)")
		kubeconfig := fs.String("kubeconfig", "", "kubeconfig path for --live")
		contextName := fs.String("context", "", "kube context for --live")
		cluster := fs.String("cluster", "acme-prod", "cluster name label")
		out := fs.String("out", "baseline.json", "output snapshot path")
		phase := fs.String("phase", "baseline", "snapshot phase: baseline|observed|deployed")
		metaPath := fs.String("meta", "", "optional JSON file merged into snapshot meta (e.g. observed_failures)")
		allowPartial := fs.Bool("allow-partial", false, "opt-in: skip malformed YAML (default fail-closed)")
		fs.SetOutput(os.Stderr)
		if err := fs.Parse(args[1:]); err != nil {
			return 2
		}
		if !*live && *dir == "" {
			fmt.Fprintln(os.Stderr, "--dir required (or pass --live)")
			return 2
		}
		var extra map[string]any
		if *metaPath != "" {
			var err error
			extra, err = collect.LoadMetaFile(*metaPath)
			if err != nil {
				fmt.Fprintf(os.Stderr, "meta: %v\n", err)
				return 2
			}
		}
		var snap *graph.Snapshot
		var err error
		if *live {
			snap, err = collect.K8sFromLive(nil, collect.LiveOptions{
				Kubeconfig:   *kubeconfig,
				Context:      *contextName,
				Cluster:      *cluster,
				AllowPartial: *allowPartial,
				Phase:        graph.Phase(*phase),
				ExtraMeta:    extra,
			})
		} else {
			snap, err = collect.K8sFromManifests(nil, *dir, collect.K8sOptions{
				ClusterName:  *cluster,
				Phase:        graph.Phase(*phase),
				AllowPartial: *allowPartial,
				ExtraMeta:    extra,
			})
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "collect: %v\n", err)
			return 5
		}
		b, err := json.MarshalIndent(snap, "", "  ")
		if err != nil {
			return 5
		}
		if err := os.WriteFile(*out, b, 0o644); err != nil {
			return 5
		}
		fmt.Fprintf(os.Stderr, "wrote snapshot %s phase=%s source=%s (%d nodes)\n", *out, snap.Phase, snap.Source, len(snap.Nodes))
		return 0
	default:
		fmt.Fprintf(os.Stderr, "unknown snapshot source: %s\n", args[0])
		return 2
	}
}

func cmdMerge(args []string) int {
	if code := requireAction(rbac.ActionMerge); code != 0 {
		return code
	}
	fs := flag.NewFlagSet("merge", flag.ContinueOnError)
	name := fs.String("name", "fleet", "merged multi-cluster name")
	out := fs.String("out", "merged.json", "output snapshot")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	paths := fs.Args()
	if len(paths) < 1 {
		fmt.Fprintln(os.Stderr, "usage: rehearsal merge --name fleet --out merged.json snap1.json [snap2.json ...]")
		return 2
	}
	var snaps []*graph.Snapshot
	for _, p := range paths {
		s, err := loader.LoadSnapshot(p)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", p, err)
			return 2
		}
		snaps = append(snaps, s)
	}
	merged := graph.MergeSnapshots(*name, snaps...)
	raw, err := json.MarshalIndent(merged, "", "  ")
	if err != nil {
		return 5
	}
	if err := os.WriteFile(*out, raw, 0o644); err != nil {
		return 5
	}
	fmt.Fprintf(os.Stderr, "wrote multi-cluster snapshot %s (%d nodes from %d clusters)\n", *out, len(merged.Nodes), len(paths))
	return 0
}

func cmdStore(args []string) int {
	if code := requireAction(rbac.ActionStore); code != 0 {
		return code
	}
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: rehearsal store list|save --root DIR")
		return 2
	}
	switch args[0] {
	case "list":
		fs := flag.NewFlagSet("store-list", flag.ContinueOnError)
		root := fs.String("root", "out/runs", "store root")
		_ = fs.Parse(args[1:])
		st, err := store.NewFS(*root)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%v\n", err)
			return 5
		}
		runs, err := st.ListRuns()
		if err != nil {
			return 5
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(runs)
		return 0
	case "save":
		fs := flag.NewFlagSet("store-save", flag.ContinueOnError)
		root := fs.String("root", "out/runs", "store root")
		reportPath := fs.String("report", "", "analyze report.json")
		evidenceDir := fs.String("evidence", "", "evidence directory")
		_ = fs.Parse(args[1:])
		if *reportPath == "" {
			return 2
		}
		raw, err := os.ReadFile(*reportPath)
		if err != nil {
			return 2
		}
		var rep analyze.Report
		if err := json.Unmarshal(raw, &rep); err != nil {
			return 2
		}
		st, err := store.NewFS(*root)
		if err != nil {
			return 5
		}
		path, err := st.SaveFromReport(&rep, *evidenceDir, rbac.ActorFromEnv())
		if err != nil {
			return 5
		}
		fmt.Println(path)
		return 0
	default:
		return 2
	}
}

func cmdAudit(args []string) int {
	if code := requireAction(rbac.ActionAuditRead); code != 0 {
		return code
	}
	fs := flag.NewFlagSet("audit", flag.ContinueOnError)
	root := fs.String("root", "out/runs", "store root")
	limit := fs.Int("limit", 50, "max events")
	_ = fs.Parse(args)
	st, err := store.NewFS(*root)
	if err != nil {
		return 5
	}
	evs, err := st.ReadAudit(*limit)
	if err != nil {
		return 5
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(evs)
	return 0
}

func cmdSign(args []string) int {
	if code := requireAction(rbac.ActionSign); code != 0 {
		return code
	}
	fs := flag.NewFlagSet("sign", flag.ContinueOnError)
	reportPath := fs.String("report", "", "analyze report.json")
	out := fs.String("out", "signed-evidence.json", "output envelope")
	_ = fs.Parse(args)
	if *reportPath == "" {
		return 2
	}
	raw, err := os.ReadFile(*reportPath)
	if err != nil {
		return 2
	}
	var rep analyze.Report
	if err := json.Unmarshal(raw, &rep); err != nil {
		return 2
	}
	sec := evidence.SecretFromEnv()
	if len(sec) == 0 {
		fmt.Fprintln(os.Stderr, "REHEARSAL_HMAC_SECRET required")
		return 2
	}
	env, err := evidence.SignReportHMAC(&rep, sec, "env")
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 5
	}
	b, _ := json.MarshalIndent(env, "", "  ")
	if err := os.WriteFile(*out, b, 0o644); err != nil {
		return 5
	}
	ok, _ := evidence.VerifyHMAC(env, sec)
	fmt.Fprintf(os.Stderr, "wrote %s verified=%v\n", *out, ok)
	return 0
}

func cmdVerifySign(args []string) int {
	if code := requireAction(rbac.ActionSign); code != 0 {
		return code
	}
	fs := flag.NewFlagSet("verify-sign", flag.ContinueOnError)
	path := fs.String("envelope", "", "signed evidence JSON")
	_ = fs.Parse(args)
	if *path == "" {
		fmt.Fprintln(os.Stderr, "--envelope required")
		return 2
	}
	raw, err := os.ReadFile(*path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 2
	}
	var env evidence.SignedEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		fmt.Fprintf(os.Stderr, "parse: %v\n", err)
		return 2
	}
	sec := evidence.SecretFromEnv()
	if len(sec) == 0 {
		fmt.Fprintln(os.Stderr, "REHEARSAL_HMAC_SECRET required")
		return 2
	}
	ok, err := evidence.VerifyHMAC(&env, sec)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 5
	}
	if !ok {
		fmt.Fprintln(os.Stderr, "signature INVALID")
		return 1
	}
	fmt.Println("signature OK")
	return 0
}

func cmdChange(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: rehearsal change manifests|terraform ...")
		return 2
	}
	switch args[0] {
	case "manifests":
		fs := flag.NewFlagSet("change-manifests", flag.ContinueOnError)
		base := fs.String("baseline", "", "baseline snapshot")
		dir := fs.String("dir", "", "rendered manifests directory")
		out := fs.String("out", "change.json", "output change path")
		id := fs.String("id", "change-manifests", "change id")
		title := fs.String("title", "Kubernetes manifest change", "title")
		ns := fs.String("namespace", "", "limit scope to namespace (repeat not supported; comma-separated)")
		prefix := fs.String("name-prefix", "", "limit scope to workload name prefix")
		allowRemove := fs.Bool("allow-remove", false, "allow removals within scope only (default false)")
		allowPartial := fs.Bool("allow-partial", false, "opt-in: skip malformed YAML (default fail-closed)")
		fs.SetOutput(os.Stderr)
		_ = fs.Parse(args[1:])
		if *base == "" || *dir == "" {
			fmt.Fprintln(os.Stderr, "--baseline and --dir required; use --namespace and --allow-remove for safe removals")
			return 2
		}
		b, err := loader.LoadSnapshot(*base)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%v\n", err)
			return 2
		}
		scope := change.ManifestScope{
			NamePrefix:   *prefix,
			AllowRemove:  *allowRemove,
			AllowPartial: *allowPartial,
		}
		if *ns != "" {
			for _, p := range strings.Split(*ns, ",") {
				p = strings.TrimSpace(p)
				if p != "" {
					scope.Namespaces = append(scope.Namespaces, p)
				}
			}
		}
		ch, err := change.FromManifestsDiff(b, *dir, *id, *title, scope)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%v\n", err)
			return 5
		}
		raw, _ := json.MarshalIndent(ch, "", "  ")
		if err := os.WriteFile(*out, raw, 0o644); err != nil {
			return 5
		}
		fmt.Fprintf(os.Stderr, "wrote change %s (removals=%v scope_ns=%v)\n", *out, *allowRemove, scope.Namespaces)
		return 0
	case "terraform":
		fs := flag.NewFlagSet("change-terraform", flag.ContinueOnError)
		plan := fs.String("plan", "", "terraform show -json file")
		base := fs.String("baseline", "", "optional baseline for valid seeds")
		out := fs.String("out", "change.json", "output")
		id := fs.String("id", "change-tf", "id")
		title := fs.String("title", "Terraform plan", "title")
		fs.SetOutput(os.Stderr)
		_ = fs.Parse(args[1:])
		if *plan == "" {
			return 2
		}
		var b *graph.Snapshot
		if *base != "" {
			var err error
			b, err = loader.LoadSnapshot(*base)
			if err != nil {
				fmt.Fprintf(os.Stderr, "%v\n", err)
				return 2
			}
		}
		ch, err := change.FromTerraformPlan(*plan, *id, *title, b)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%v\n", err)
			return 5
		}
		raw, _ := json.MarshalIndent(ch, "", "  ")
		if err := os.WriteFile(*out, raw, 0o644); err != nil {
			return 5
		}
		fmt.Fprintf(os.Stderr, "wrote change %s\n", *out)
		return 0
	default:
		return 2
	}
}

func abs(p string) string {
	a, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	return a
}
